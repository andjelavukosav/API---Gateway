package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	blogpb "example/gateway/proto/blog"
	followerpb "example/gateway/proto/follower" // <-- generisani follower .pb fajlovi
	imagepb "example/gateway/proto/image"
	positionpb "example/gateway/proto/position"
	pb "example/gateway/proto/tours"

	"example/gateway/config"
	"example/gateway/handlers"
	"example/gateway/middleware"
	"example/gateway/proto/stakeholders"
	"example/gateway/proto/tours"

	"github.com/gorilla/mux"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	cfg := config.GetConfig()

	// -------- Stakeholders gRPC connection --------
	stakeholdersConn, err := grpc.DialContext(
		context.Background(),
		cfg.StakeholdersServiceAddress,
		//grpc.WithBlock(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalln("Failed to dial Stakeholders server:", err)
	}
	defer stakeholdersConn.Close()

	// -------- Tours gRPC connection --------
	toursConn, err := grpc.DialContext(
		context.Background(),
		cfg.ToursServiceAddress,
		//grpc.WithBlock(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalln("Failed to dial Tours server:", err)
	}
	defer toursConn.Close()

	// Inicijalizacija gRPC klijenta za Tours servis
	toursClient := pb.NewToursServiceClient(toursConn)
	toursImageClient := imagepb.NewImageServiceClient(toursConn)

	// PositionService gRPC client
	positionClient := positionpb.NewPositionServiceClient(toursConn)

	// -------- gRPC-Gateway multiplexer --------
	gwmux := runtime.NewServeMux()

	// Register Stakeholders service
	stakeholdersClient := stakeholders.NewStakeholdersServiceClient(stakeholdersConn)
	if err := stakeholders.RegisterStakeholdersServiceHandlerClient(context.Background(), gwmux, stakeholdersClient); err != nil {
		log.Fatalln("Failed to register Stakeholders gateway:", err)
	}

	// Register Tours service
	if err := tours.RegisterToursServiceHandlerClient(context.Background(), gwmux, toursClient); err != nil {
		log.Fatalln("Failed to register Tours gateway:", err)
	}

	// Register PositionService REST endpoint-a preko gRPC-Gateway
	if err := positionpb.RegisterPositionServiceHandlerClient(context.Background(), gwmux, positionClient); err != nil {
		log.Fatalln("Failed to register PositionService gateway:", err)
	}

	// -------- Blogs gRPC connection --------
	blogConn, err := grpc.DialContext(
		context.Background(),
		cfg.BlogServiceAddress,
		//grpc.WithBlock(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalln("Failed to dial BlogService:", err)
	}
	defer blogConn.Close()

	followerConn, err := grpc.DialContext(
		context.Background(),
		cfg.FollowerServiceAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalln("Failed to dial Follower server:", err)
	}
	defer followerConn.Close()

	followerClient := followerpb.NewFollowerServiceClient(followerConn)

	// Blog REST preko gRPC client (sve rute osim multipart create)
	blogClient := blogpb.NewBlogServiceClient(blogConn)
	imageClient := imagepb.NewImageServiceClient(blogConn)

	// Registracija Blog servisa (sve osim multipart create)
	if err = blogpb.RegisterBlogServiceHandlerClient(context.Background(), gwmux, blogClient); err != nil {
		log.Fatalln("Failed to register BlogService gateway:", err)
	}

	if err := followerpb.RegisterFollowerServiceHandlerClient(
		context.Background(), gwmux, followerClient,
	); err != nil {
		log.Fatalln("Failed to register Follower gateway:", err)
	}

	// ----- REST handler za multipart POST (create blog) -----
	blogHandler := handlers.NewBlogGatewayHandler(blogClient, imageClient)

	// ---------------- Tour REST handler ----------------
	tourHandler := handlers.NewTourGatewayHandler(toursClient, toursImageClient)

	r := mux.NewRouter()

	r.HandleFunc("/tours/add-keypoint", tourHandler.AddKeyPointHandler).Methods("POST", "OPTIONS")
	r.HandleFunc("/tours/tour/{tourId}/update-keypoint", tourHandler.UpdateKeyPointHandler).Methods("PUT", "OPTIONS")
	r.PathPrefix("/tours/uploads/").HandlerFunc(tourHandler.DownloadImageHandler).Methods("GET")

	r.HandleFunc("/blogs/create-blog", blogHandler.CreateBlogHandler).Methods("POST", "OPTIONS")
	r.HandleFunc("/blogs/user", blogHandler.GetUserBlogsHandler).Methods("GET", "OPTIONS")
	r.HandleFunc("/blogs/{blog_id}/comments", blogHandler.CreateCommentHandler).Methods("POST", "OPTIONS")
	r.PathPrefix("/blogs/uploads/").HandlerFunc(blogHandler.DownloadImageHandler).Methods("GET")

	// gRPC Gateway fallback za ostale rute
	r.PathPrefix("/").Handler(gwmux)

	// Omotaj router u CORS middleware
	handler := middleware.CORSMiddleware(r)

	// -------- HTTP server --------
	gwServer := &http.Server{
		Addr:    cfg.Address,
		Handler: handler,
	}

	go func() {
		log.Println("Starting Gateway on", cfg.Address)
		if err := gwServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("server error: ", err)
		}
	}()

	// Graceful shutdown
	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, syscall.SIGTERM, os.Interrupt)
	<-stopCh

	if err := gwServer.Close(); err != nil {
		log.Fatalln("error while stopping server: ", err)
	}
}
