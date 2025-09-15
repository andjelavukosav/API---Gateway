package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"log"
	"path/filepath"

	"example/gateway/util"
	pb "example/gateway/proto/blog"
	imagepb "example/gateway/proto/image"
    "github.com/gorilla/mux"
)

type BlogGatewayHandler struct {
	BlogClient pb.BlogServiceClient
	ImageClient imagepb.ImageServiceClient
}

func NewBlogGatewayHandler(blogClient pb.BlogServiceClient, imageClient imagepb.ImageServiceClient) *BlogGatewayHandler {
	return &BlogGatewayHandler{BlogClient: blogClient, ImageClient: imageClient}
}

// REST endpoint za frontend: POST /api/blogs/create-blog
func (h *BlogGatewayHandler) CreateBlogHandler(w http.ResponseWriter, r *http.Request) {
	userID, err := util.ExtractUserIDFromToken(r) 
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	err = r.ParseMultipartForm(20 << 20) // 20MB
	if err != nil {
		log.Println("Failed to parse form:", err)
		http.Error(w, "failed to parse form: "+err.Error(), http.StatusBadRequest)
		return
	}

	title := r.FormValue("title")
	description := r.FormValue("description")

	// --- Obrada fajlova ---
	var imagePaths []string
	files := r.MultipartForm.File["images"] // frontend salje kao "images"
	for _, fileHeader := range files {
        file, err := fileHeader.Open()
        if err != nil {
            http.Error(w, "could not open file", http.StatusBadRequest)
            return
        }

        // gRPC streaming upload
        stream, err := h.ImageClient.UploadImage(context.Background())
        if err != nil {
			file.Close() // zatvaramo ako upload ne uspije
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

        // zatim šaljemo chunkove
        buf := make([]byte, 1024*32) // 32KB
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

		// zatvaramo file odmah nakon sto je upload gotov
		file.Close()

        // završavamo i čekamo odgovor
        res, err := stream.CloseAndRecv()
        if err != nil {
            http.Error(w, "could not finish upload", http.StatusInternalServerError)
            return
        }

        // --- ovde pravimo pun URL umesto lokalnog path ---
        filename := filepath.Base(res.Path)
        imageURL := "http://localhost:8080/blogs/uploads/" + filename
        imagePaths = append(imagePaths, imageURL)
    }

	// --- Sada se poziva Blog-Service ---
	// Kreiranje gRPC request-a
	reqGrpc := &pb.CreateBlogRequest{
		Title:       title,
		Description: description,
		UserId:      userID,
		Images:      imagePaths, //repeated string
	}

	resp, err := h.BlogClient.CreateBlog(context.Background(), reqGrpc)
	if err != nil {
		http.Error(w, "failed to create blog: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// šaljemo JSON nazad frontendu
    json.NewEncoder(w).Encode(resp)
}

func (h *BlogGatewayHandler) GetUserBlogsHandler(w http.ResponseWriter, r *http.Request) {
    //Uzmi userID iz tokena 
	userID, err := util.ExtractUserIDFromToken(r)
    if err != nil {
        http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
        return
    }

	// Napravi gRPC request i ubaci userID
    req := &pb.GetUserBlogsRequest{
        UserId: userID,
    }

    // Pozovi gRPC metod
    resp, err := h.BlogClient.GetUserBlogs(r.Context(), req)
    if err != nil {
        http.Error(w, "failed to get user blogs: "+err.Error(), http.StatusInternalServerError)
        return
    }

    // --- Transformiši putanje u URL-ove ---
    for _, blog := range resp.Blogs {
        for i, path := range blog.Images {
            filename := filepath.Base(path) // npr. "myimg.png"
            blog.Images[i] = "http://localhost:8080/blogs/uploads/" + filename
        }
    }

    // Vrati JSON nazad frontendu
    json.NewEncoder(w).Encode(resp.Blogs)
}

func (h *BlogGatewayHandler) DownloadImageHandler(w http.ResponseWriter, r *http.Request) {
    // očekujemo GET /uploads/{filename}
    filename := r.URL.Path[len("/blogs/uploads/"):] // uzmi samo ime fajla
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

    // čuvamo u buffer i detektujemo content-type
    var contentType string
   // wbuf := make([]byte, 0)

    for {
        resp, err := stream.Recv()
        if err == io.EOF {
            break
        }
        if err != nil {
            http.Error(w, "download error: "+err.Error(), http.StatusInternalServerError)
            return
        }

        // prvi chunk koristiš da postaviš Content-Type
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

func (h *BlogGatewayHandler) CreateCommentHandler(w http.ResponseWriter, r *http.Request){
    // 1. Uzimamo userId iz JWT
    userID, err := util.ExtractUserIDFromToken(r)
    if err != nil {
        http.Error(w, "Unauthorized", http.StatusUnauthorized)
        return
    }

    // 2. Uzimamo blogId iz URL path-a
    vars := mux.Vars(r)
    blogID := vars["blog_id"]

    // 3. Parse body da uzmemo content
    var body struct {
        Content string `json:"content"`
    }
    if err = json.NewDecoder(r.Body).Decode(&body); err != nil {
        http.Error(w, "Invalid request body", http.StatusBadRequest)
        return
    }

    // 4. Sad tek pravimo gRPC request
    grpcReq := &pb.CreateCommentRequest{
        BlogId: blogID,
        Content: body.Content,
        UserId: userID,
    }

    // 5. Pozivamo gRPC BlogService
    resp, err := h.BlogClient.CreateComment(r.Context(), grpcReq)
    if err != nil {
        log.Println("gRPC CreateComment error:", err)

        http.Error(w, "Failed to create comment", http.StatusInternalServerError)
        return
    }

    // 6. Vrati response frontendu
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(resp)
}