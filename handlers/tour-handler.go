package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	imagepb "example/gateway/proto/image"
	shopping_cart "example/gateway/proto/shopping-cart"
	pb "example/gateway/proto/tours"

	"github.com/gorilla/mux"
)

type TourGatewayHandler struct {
	ToursClient  pb.ToursServiceClient
	ImageClient  imagepb.ImageServiceClient
	OrdersClient shopping_cart.OrdersServiceClient
}

func NewTourGatewayHandler(
	toursClient pb.ToursServiceClient,
	imageClient imagepb.ImageServiceClient,
	ordersClient shopping_cart.OrdersServiceClient,
) *TourGatewayHandler {
	return &TourGatewayHandler{
		ToursClient:  toursClient,
		ImageClient:  imageClient,
		OrdersClient: ordersClient,
	}
}

// ----------------- TOUR METODE (koriste pb i ToursClient) -----------------

func (h *TourGatewayHandler) AddKeyPointHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "Error parsing form: "+err.Error(), http.StatusBadRequest)
		return
	}

	imagePath, err := h.handleFileUpload(r, "file")
	if err != nil {
		http.Error(w, "file upload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	tourId := r.FormValue("tourId")
	keyPoint, _ := parseKeyPointFromForm(r, imagePath)

	req := &pb.AddKeyPointRequest{
		TourId: tourId,
		Point:  keyPoint,
	}

	tourResp, err := h.ToursClient.AddKeyPoint(r.Context(), req)
	if err != nil {
		http.Error(w, "Error adding key point: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tourResp)
}

func (h *TourGatewayHandler) DownloadImageHandler(w http.ResponseWriter, r *http.Request) {
	filename := r.URL.Path[len("/tours/uploads/"):] // izvuče samo ime fajla
	if filename == "" {
		http.Error(w, "filename required", http.StatusBadRequest)
		return
	}
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

func (h *TourGatewayHandler) UpdateKeyPointHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	tourId := vars["tourId"]
	if tourId == "" {
		http.Error(w, "tourId is required", http.StatusBadRequest)
		return
	}

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

	req := &pb.UpdateKeyPointRequest{
		TourId:   tourId,
		KeyPoint: keyPoint,
	}

	resp, err := h.ToursClient.UpdateKeyPoint(r.Context(), req)
	if err != nil {
		http.Error(w, "failed to update key point: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ----------------- POMOĆNE FUNKCIJE (ostaju iste) -----------------

func (h *TourGatewayHandler) handleFileUpload(r *http.Request, fieldName string) (string, error) {
	file, header, err := r.FormFile(fieldName)
	if err != nil {
		if err == http.ErrMissingFile {
			return "", nil
		}
		return "", err
	}
	defer file.Close()

	stream, err := h.ImageClient.UploadImage(r.Context())
	if err != nil {
		return "", err
	}

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
		Id:          id,
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

/*func (h *TourGatewayHandler) GetPurchasedToursHandler(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("userId")
	log.Println("🎯 [Gateway] GetPurchasedToursHandler called with userId:", userID)

	if userID == "" {
		http.Error(w, "userId is required", http.StatusBadRequest)
		return
	}

	ordersResp, err := h.OrdersClient.GetPurchasedTours(r.Context(), &shopping_cart.GetPurchasedToursRequest{
		UserId: userID,
	})
	if err != nil {
		log.Println("❌ Orders service error:", err)
		http.Error(w, "failed to fetch purchased tours from Orders service: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Println("✅ Orders returned tourIds:", ordersResp.TourIds)

	var tours []*pb.TourResponse
	for _, tourID := range ordersResp.TourIds {
		tourResp, err := h.ToursClient.GetTourById(r.Context(), &pb.GetTourByIdRequest{Id: tourID})
		if err != nil {
			log.Println("⚠️ Failed to fetch tour details for", tourID, ":", err)
			continue
		}
		tours = append(tours, tourResp)
	}

	log.Println("✅ Returning", len(tours), "tours to frontend")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tours)
}
*/

func (h *TourGatewayHandler) GetPurchasedToursHandler(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("userId")
	log.Println("🎯 [Gateway] GetPurchasedToursHandler called with userId:", userID)

	if userID == "" {
		http.Error(w, "userId is required", http.StatusBadRequest)
		return
	}

	ordersResp, err := h.OrdersClient.GetPurchasedTours(r.Context(), &shopping_cart.GetPurchasedToursRequest{
		UserId: userID,
	})
	if err != nil {
		log.Println("❌ Orders service error:", err)
		http.Error(w, "failed to fetch purchased tours from Orders service: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Println("✅ Orders returned tourIds:", ordersResp.TourIds)

	var tours []*pb.TourResponse
	for _, tourID := range ordersResp.TourIds {
		// Proveri da li postoji bilo koja sesija za korisnika i ovu turu
		hasExecResp, err := h.ToursClient.HasTourExecution(r.Context(), &pb.HasTourExecutionRequest{
			UserId: userID,
			TourId: tourID,
		})
		if err != nil {
			log.Println("❌ Error calling HasTourExecution for tour", tourID, ":", err)
			// obradi grešku, npr. continue ili return
			continue
		}

		if hasExecResp.HasExecution {
			log.Println("⏩ Skipping tour", tourID, "because user already has a session")
			continue
		}

		tourResp, err := h.ToursClient.GetTourById(r.Context(), &pb.GetTourByIdRequest{Id: tourID})
		if err != nil {
			log.Println("⚠️ Failed to fetch tour details for", tourID, ":", err)
			continue
		}
		tours = append(tours, tourResp)
	}

	log.Println("✅ Returning", len(tours), "tours to frontend")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tours)
}
