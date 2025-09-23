package handlers

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"io"
	"strconv"
	"strings"
	"fmt"
	"log"

	pb "example/gateway/proto/tours"
	imagepb "example/gateway/proto/image"
	"example/gateway/util"

	"github.com/gorilla/mux"
	"google.golang.org/protobuf/encoding/protojson"
)

type TourGatewayHandler struct {
	ToursClient pb.ToursServiceClient
	ImageClient imagepb.ImageServiceClient
}

func NewTourGatewayHandler(toursClient pb.ToursServiceClient, imageClient imagepb.ImageServiceClient) *TourGatewayHandler {
	return &TourGatewayHandler{
		ToursClient: toursClient,
		ImageClient: imageClient,
	}
}

func (h *TourGatewayHandler) CreateTourHandler(w http.ResponseWriter, r *http.Request) {
	// Izvlacimo userID iz tokena
	userID, err := util.ExtractUserIDFromToken(r)
	if err != nil {
		http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
        return
	}
	log.Printf("Extracted userID from token: %s", userID)

	// Dekodiramo JSON body u protobuf CreateTourRequest
	var req pb.CreateTourRequest 
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body: "+err.Error(), http.StatusBadRequest)
        return
	}
	log.Printf("Raw request body: %s", string(body))

	// pretvaramo JSON podatke u Go strukturu
	if err := json.Unmarshal(body, &req); err != nil {
        http.Error(w, "Invalid JSON body: "+err.Error(), http.StatusBadRequest)
        return
    }
	log.Printf("Decoded JSON -> req: %+v", req)


	req.AuthorId = userID
	log.Printf("Final req (after overriding AuthorId): %+v", req)


	createdTour, err := h.ToursClient.CreateTour(r.Context(), &req)
	if err != nil {
		http.Error(w, "Failed to create tour: "+err.Error(), http.StatusInternalServerError)
        return
	}
	log.Printf("Created tour response: %+v", createdTour)


	// Vracamo createdTour kao JSON
	w.Header().Set("Content-Type", "application/json")
    marshaler := protojson.MarshalOptions{
		UseEnumNumbers: false, // važno! ovo vraća enum kao string, ne broj
	}
	jsonBytes, err := marshaler.Marshal(createdTour)
	if err != nil {
		http.Error(w, "Failed to marshal protobuf: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(jsonBytes)
}

func (h *TourGatewayHandler) AddKeyPointHandler(w http.ResponseWriter, r *http.Request) {
	// 1️. Parsiranje multipart forme (max 10 MB)
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "Error parsing form: "+err.Error(), http.StatusBadRequest)
		return
	}

	imagePath, err := h.handleFileUpload(r, "file")
	if err != nil {
		http.Error(w, "file upload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 4️. Preuzimanje ostalih polja iz forme i kreiranje KeyPoint
	tourId := r.FormValue("tourId")
	keyPoint, _ := parseKeyPointFromForm(r, imagePath)

	// 5️. Kreiranje AddKeyPointRequest
	req := &pb.AddKeyPointRequest{
		TourId: tourId,
		Point: keyPoint,
	}

	// 6️. Poziv ToursService
	tourResp, err := h.ToursClient.AddKeyPoint(r.Context(), req)
	if err != nil {
		http.Error(w, "Error adding key point: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 7️. Odgovor frontendu
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tourResp)
}


func (h *TourGatewayHandler) DownloadImageHandler(w http.ResponseWriter, r *http.Request) {
    // očekujemo GET /tours/uploads/{filename}
    filename := r.URL.Path[len("/tours/uploads/"):] // izvuče samo ime fajla
    if filename == "" {
        http.Error(w, "filename required", http.StatusBadRequest)
        return
    }

    // gRPC request ka Tours microservice
    req := &imagepb.DownloadImageRequest{Filename: filename}
    stream, err := h.ImageClient.DownloadImage(r.Context(), req)
    if err != nil {
        http.Error(w, "failed to start download: "+err.Error(), http.StatusInternalServerError)
        return
    }

    var contentType string

    for {
        resp, err := stream.Recv()
        if err == io.EOF {
            break
        }
        if err != nil {
            http.Error(w, "download error: "+err.Error(), http.StatusInternalServerError)
            return
        }

        if contentType == "" {
            contentType = resp.ContentType
            w.Header().Set("Content-Type", contentType)
            w.Header().Set("Content-Disposition", "inline; filename=\""+filename+"\"")
        }

        _, err = w.Write(resp.ChunkData)
        if err != nil {
            return
        }
    }
}

func (h *TourGatewayHandler) UpdateKeyPointHandler(w http.ResponseWriter, r *http.Request){
	// Preuzmi tourId iz putanje
	vars := mux.Vars(r)
	tourId := vars["tourId"]
	if tourId == ""{
		http.Error(w, "tourId is required", http.StatusBadRequest)
        return
	}

	// Parsiranje KeyPoint-a
	var keyPoint *pb.KeyPoint
	var err error

	contentType := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(contentType, "multipart/form-data"):
		keyPoint, err = h.ParseMultipartKeyPoint(r)
	case strings.HasPrefix(contentType, "application/json"):
		keyPoint, err = h.parseJSONKeyPoint(r)
	default:
		http.Error(w, "unsupported Content-Type", http.StatusBadRequest)
		return
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

    // Kreiranje UpdateKeyPointRequest, i pravljenje gRPC requesta
    req := &pb.UpdateKeyPointRequest{
        TourId:   tourId,
        KeyPoint: keyPoint,
    }

	// Poziv gRPC servera
    resp, err := h.ToursClient.UpdateKeyPoint(r.Context(), req)
    if err != nil {
        http.Error(w, "failed to update key point: "+err.Error(), http.StatusInternalServerError)
        return
    }

    // Odgovor frontendu
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(resp)
}

func (h *TourGatewayHandler) handleFileUpload(r *http.Request, fieldName string) (string, error) {
	file, header, err := r.FormFile(fieldName)
	if err != nil {
		if err == http.ErrMissingFile {
			return "", nil // fajl nije obavezan
		}
		return "", err
	}
	defer file.Close()

	stream, err := h.ImageClient.UploadImage(r.Context())
	if err != nil {
		return "", err
	}

	// Pošalji info
	if err := stream.Send(&imagepb.UploadImageRequest{
		RequestData: &imagepb.UploadImageRequest_Info{
			Info: &imagepb.ImageInfo{
				Filename:  header.Filename,
				ImageType: filepath.Ext(header.Filename)[1:],
			},
		},
	}); err != nil {
		return "", err
	}

	buf := make([]byte, 32*1024)
	for {
		n, err := file.Read(buf)
		if n > 0 {
			if err := stream.Send(&imagepb.UploadImageRequest{
				RequestData: &imagepb.UploadImageRequest_ChunkData{
					ChunkData: buf[:n],
				},
			}); err != nil {
				return "", err
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", err
		}
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		return "", err
	}

	return strings.TrimPrefix(resp.Path, "uploads/"), nil
}

func parseKeyPointFromForm(r *http.Request, imageURL string) (*pb.KeyPoint, error) {
	id := r.FormValue("id")
	name := r.FormValue("name")
	description := r.FormValue("description")
	orderStr := r.FormValue("order")
	orderInt64, _ := strconv.ParseInt(orderStr, 10, 32)
	order := int32(orderInt64)

	lat, _ := strconv.ParseFloat(r.FormValue("latitude"), 64)
	lng, _ := strconv.ParseFloat(r.FormValue("longitude"), 64)

	return &pb.KeyPoint{
		Id:			 id,
		Name:        name,
		Description: description,
		Latitude:    lat,
		Longitude:   lng,
		Order:       order,
		ImageURL:    imageURL,
	}, nil
}

func (h *TourGatewayHandler) ParseMultipartKeyPoint(r *http.Request) (*pb.KeyPoint, error) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		return nil, fmt.Errorf("error parsing form: %w", err)
	}

	imagePath, err := h.handleFileUpload(r, "file")
	if err != nil {
		return nil, fmt.Errorf("file upload failed: %w", err)
	}

	return parseKeyPointFromForm(r, imagePath)
	
}

func (h *TourGatewayHandler) parseJSONKeyPoint(r *http.Request) (*pb.KeyPoint, error) {
	var reqBody struct {
		Id          string  `json:"id"`
		Name        string  `json:"name"`
		Description string  `json:"description"`
		Latitude    float64 `json:"latitude"`
		Longitude   float64 `json:"longitude"`
		ImageURL    string  `json:"imageURL"`
		Order       int32   `json:"order"`
	}

	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	return &pb.KeyPoint{
		Id:          reqBody.Id,
		Name:        reqBody.Name,
		Description: reqBody.Description,
		Latitude:    reqBody.Latitude,
		Longitude:   reqBody.Longitude,
		ImageURL:    reqBody.ImageURL,
		Order:       reqBody.Order,
	}, nil
}

func (h *TourGatewayHandler) UpdateTourStatusHandler(w http.ResponseWriter, r *http.Request) {
    vars := mux.Vars(r)
    tourId := vars["tourId"]
    if tourId == "" {
        http.Error(w, "tourId is required", http.StatusBadRequest)
        return
    }

    userID, err := util.ExtractUserIDFromToken(r)
    if err != nil {
        http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
        return
    }

	var body struct {
		NewStatus string `json:"newStatus"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var status pb.TourStatus
	switch strings.ToUpper(body.NewStatus) {
	case "PUBLISHED":
		status = pb.TourStatus_PUBLISHED
	case "ARCHIVED":
		status = pb.TourStatus_ARCHIVED
	default:
		http.Error(w, "Invalid status", http.StatusBadRequest)
		return
	}

    req := &pb.UpdateTourStatusRequest{
        TourId:   tourId,
        AuthorId: userID,
		NewStatus: status,
    }

    resp, err := h.ToursClient.UpdateTourStatus(r.Context(), req)
    if err != nil {
        http.Error(w, "Failed to update tour status: "+err.Error(), http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(resp)
}

