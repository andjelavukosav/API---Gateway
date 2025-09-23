// handlers/review-handler.go
package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	imagepb "example/gateway/proto/image"
	reviewpb "example/gateway/proto/review"
	"example/gateway/util"

	"github.com/gorilla/mux"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ReviewHandler struct {
	ReviewClient reviewpb.ReviewServiceClient
	ImageClient  imagepb.ImageServiceClient
}

func NewReviewHandler(reviewClient reviewpb.ReviewServiceClient, imageClient imagepb.ImageServiceClient) *ReviewHandler {
	return &ReviewHandler{
		ReviewClient: reviewClient,
		ImageClient:  imageClient,
	}
}

// CreateReviewHandler kreira novu recenziju sa uploadom slika
func (h *ReviewHandler) CreateReviewHandler(w http.ResponseWriter, r *http.Request) {
	userID, err := util.ExtractUserIDFromToken(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	err = r.ParseMultipartForm(20 << 20) // 20MB
	if err != nil {
		http.Error(w, "failed to parse form: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Form values
	ratingStr := r.FormValue("rating")
	comment := r.FormValue("comment")
	tourID := r.FormValue("tour_id")
	touristName := r.FormValue("tourist_name")
	touristImage := r.FormValue("tourist_image")
	visitDateStr := r.FormValue("visit_date")

	rating, _ := strconv.Atoi(ratingStr)

	// Parsiranje datuma
	var visitDate *timestamppb.Timestamp
	if visitDateStr != "" {
		t, err := time.Parse(time.RFC3339, visitDateStr)
		if err != nil {
			http.Error(w, "invalid visit_date format: "+err.Error(), http.StatusBadRequest)
			return
		}
		visitDate = timestamppb.New(t)
	}

	// --- Obrada fajlova ---
	var imagePaths []string
	files := r.MultipartForm.File["images"] // frontend šalje kao "images"
	for _, fileHeader := range files {
		file, err := fileHeader.Open()
		if err != nil {
			http.Error(w, "could not open file", http.StatusBadRequest)
			return
		}

		// gRPC streaming upload
		stream, err := h.ImageClient.UploadImage(context.Background())
		if err != nil {
			file.Close()
			http.Error(w, "could not start upload", http.StatusInternalServerError)
			return
		}

		// prvo šaljemo info
		err = stream.Send(&imagepb.UploadImageRequest{
			RequestData: &imagepb.UploadImageRequest_Info{
				Info: &imagepb.ImageInfo{
					Filename:  fileHeader.Filename,
					ImageType: filepath.Ext(fileHeader.Filename),
				},
			},
		})
		if err != nil {
			http.Error(w, "could not send image info", http.StatusInternalServerError)
			return
		}

		// šaljemo chunkove
		buf := make([]byte, 32*1024)
		for {
			n, err := file.Read(buf)
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, "could not read file", http.StatusInternalServerError)
				return
			}
			stream.Send(&imagepb.UploadImageRequest{
				RequestData: &imagepb.UploadImageRequest_ChunkData{
					ChunkData: buf[:n],
				},
			})
		}

		file.Close()

		// čekamo odgovor
		res, err := stream.CloseAndRecv()
		if err != nil {
			http.Error(w, "could not finish upload", http.StatusInternalServerError)
			return
		}

		// pravimo URL do slike
		filename := filepath.Base(res.Path)
		imageURL := "http://localhost:8080/tours/uploads/" + filename
		imagePaths = append(imagePaths, imageURL)
	}

	// --- gRPC poziv Review servisu ---
	reqGrpc := &reviewpb.CreateReviewRequest{
		Rating:       int32(rating),
		Comment:      comment,
		TouristId:    userID,
		TourId:       tourID,
		VisitDate:    visitDate,
		Images:       imagePaths,
		TouristName:  touristName,
		TouristImage: touristImage,
	}

	resp, err := h.ReviewClient.CreateReview(r.Context(), reqGrpc)
	if err != nil {
		http.Error(w, "failed to create review: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

// GetReviewHandler preuzima recenziju po ID-u
func (h *ReviewHandler) GetReviewHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]
	if id == "" {
		http.Error(w, "Review ID is required", http.StatusBadRequest)
		return
	}

	req := &reviewpb.GetReviewRequest{Id: id}
	resp, err := h.ReviewClient.GetReview(r.Context(), req)
	if err != nil {
		http.Error(w, "Error getting review: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// GetReviewsByTourHandler preuzima sve recenzije za određenu turu
func (h *ReviewHandler) GetReviewsByTourHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	tourID := vars["tour_id"]
	if tourID == "" {
		http.Error(w, "Tour ID is required", http.StatusBadRequest)
		return
	}

	req := &reviewpb.GetReviewsByTourRequest{TourId: tourID}
	resp, err := h.ReviewClient.GetReviewsByTour(r.Context(), req)
	if err != nil {
		http.Error(w, "Error getting reviews by tour: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// --- Transformacija putanja u URL-ove ---
	for _, review := range resp.Reviews {
		for i, path := range review.Images {
			filename := filepath.Base(path) // npr. "myimg.png"
			review.Images[i] = "http://localhost:8080/reviews/uploads/" + filename
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp.Reviews)
}

// DownloadReviewImageHandler omogućava preuzimanje slika recenzija
func (h *ReviewHandler) DownloadReviewImageHandler(w http.ResponseWriter, r *http.Request) {
	// očekujemo GET /reviews/uploads/{filename}
	filename := r.URL.Path[len("/reviews/uploads/"):] // isečemo deo puta
	if filename == "" {
		http.Error(w, "filename required", http.StatusBadRequest)
		return
	}

	// gRPC request
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

		// prvi chunk postavlja content-type
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

// GetReviewsByTouristHandler preuzima sve recenzije određenog turiste
func (h *ReviewHandler) GetReviewsByTouristHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	touristID := vars["tourist_id"]
	if touristID == "" {
		http.Error(w, "Tourist ID is required", http.StatusBadRequest)
		return
	}

	req := &reviewpb.GetReviewsByTouristRequest{TouristId: touristID}
	resp, err := h.ReviewClient.GetReviewsByTourist(r.Context(), req)
	if err != nil {
		http.Error(w, "Error getting reviews by tourist: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// UpdateReviewHandler ažurira postojeću recenziju
func (h *ReviewHandler) UpdateReviewHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]
	if id == "" {
		http.Error(w, "Review ID is required", http.StatusBadRequest)
		return
	}

	var reqBody struct {
		Rating       int32    `json:"rating"`
		Comment      string   `json:"comment"`
		VisitDate    string   `json:"visit_date"`
		Images       []string `json:"images"`
		TouristName  string   `json:"tourist_name"`
		TouristImage string   `json:"tourist_image"`
	}

	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Parsiranje datuma
	var visitDate *timestamppb.Timestamp
	if reqBody.VisitDate != "" {
		t, err := time.Parse(time.RFC3339, reqBody.VisitDate)
		if err != nil {
			http.Error(w, "Invalid visit_date format, use ISO format: "+err.Error(), http.StatusBadRequest)
			return
		}
		visitDate = timestamppb.New(t)
	}

	req := &reviewpb.UpdateReviewRequest{
		Id:           id,
		Rating:       reqBody.Rating,
		Comment:      reqBody.Comment,
		VisitDate:    visitDate,
		Images:       reqBody.Images,
		TouristName:  reqBody.TouristName,
		TouristImage: reqBody.TouristImage,
	}

	resp, err := h.ReviewClient.UpdateReview(r.Context(), req)
	if err != nil {
		http.Error(w, "Error updating review: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// DeleteReviewHandler briše recenziju
func (h *ReviewHandler) DeleteReviewHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]
	if id == "" {
		http.Error(w, "Review ID is required", http.StatusBadRequest)
		return
	}

	req := &reviewpb.DeleteReviewRequest{Id: id}
	resp, err := h.ReviewClient.DeleteReview(r.Context(), req)
	if err != nil {
		http.Error(w, "Error deleting review: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// GetAverageRatingHandler preuzima prosečnu ocenu za turu
func (h *ReviewHandler) GetAverageRatingHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	tourID := vars["tour_id"]
	if tourID == "" {
		http.Error(w, "Tour ID is required", http.StatusBadRequest)
		return
	}

	req := &reviewpb.GetAverageRatingRequest{TourId: tourID}
	resp, err := h.ReviewClient.GetAverageRating(r.Context(), req)
	if err != nil {
		http.Error(w, "Error getting average rating: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
