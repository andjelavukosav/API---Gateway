package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	// Proto paketi
	blogpb "example/gateway/proto/blog"
	imagepb "example/gateway/proto/image"
	positionpb "example/gateway/proto/position"
	stakeholderspb "example/gateway/proto/stakeholders"
	tourspb "example/gateway/proto/tours"
	orderspb "example/gateway/proto/shopping-cart" // 👈 Orders proto

	"example/gateway/config"
	"example/gateway/handlers"
	"example/gateway/middleware"

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
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalln("Failed to dial Stakeholders server:", err)
	}
	defer stakeholdersConn.Close()
	stakeholdersClient := stakeholderspb.NewStakeholdersServiceClient(stakeholdersConn)

	// -------- Tours gRPC connection --------
	toursConn, err := grpc.DialContext(
		context.Background(),
		cfg.ToursServiceAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalln("Failed to dial Tours server:", err)
	}
	defer toursConn.Close()
	toursClient := tourspb.NewToursServiceClient(toursConn)
	toursImageClient := imagepb.NewImageServiceClient(toursConn)

	// -------- Position gRPC connection --------
	positionClient := positionpb.NewPositionServiceClient(toursConn)

	// -------- Orders gRPC connection --------
	ordersConn, err := grpc.DialContext(
		context.Background(),
		cfg.OrdersServiceAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalln("Failed to dial Orders server:", err)
	}
	defer ordersConn.Close()
	ordersClient := orderspb.NewOrdersServiceClient(ordersConn)

	// -------- Blogs gRPC connection --------
	blogConn, err := grpc.DialContext(
		context.Background(),
		cfg.BlogServiceAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalln("Failed to dial BlogService:", err)
	}
	defer blogConn.Close()
	blogClient := blogpb.NewBlogServiceClient(blogConn)
	imageClient := imagepb.NewImageServiceClient(blogConn)

	// -------- gRPC-Gateway multiplexer --------
	gwmux := runtime.NewServeMux()

	// Registracija servisa u gateway
	if err := stakeholderspb.RegisterStakeholdersServiceHandlerClient(context.Background(), gwmux, stakeholdersClient); err != nil {
		log.Fatalln("Failed to register Stakeholders gateway:", err)
	}
	if err := tourspb.RegisterToursServiceHandlerClient(context.Background(), gwmux, toursClient); err != nil {
		log.Fatalln("Failed to register Tours gateway:", err)
	}
	if err := positionpb.RegisterPositionServiceHandlerClient(context.Background(), gwmux, positionClient); err != nil {
		log.Fatalln("Failed to register Position gateway:", err)
	}
	if err := blogpb.RegisterBlogServiceHandlerClient(context.Background(), gwmux, blogClient); err != nil {
		log.Fatalln("Failed to register Blog gateway:", err)
	}
	if err := orderspb.RegisterOrdersServiceHandlerClient(context.Background(), gwmux, ordersClient); err != nil { // 👈 Orders REST
		log.Fatalln("Failed to register Orders gateway:", err)
	}

	// -------- Custom HTTP Handlers --------
	blogHandler := handlers.NewBlogGatewayHandler(blogClient, imageClient)
	tourHandler := handlers.NewTourGatewayHandler(toursClient, toursImageClient)

	r := mux.NewRouter()

	// Tours custom endpoints
	r.HandleFunc("/tours/add-keypoint", tourHandler.AddKeyPointHandler).Methods("POST", "OPTIONS")
	r.HandleFunc("/tours/tour/{tourId}/update-keypoint", tourHandler.UpdateKeyPointHandler).Methods("PUT", "OPTIONS")
	r.PathPrefix("/tours/uploads/").HandlerFunc(tourHandler.DownloadImageHandler).Methods("GET")

	// Blogs custom endpoints
	r.HandleFunc("/blogs/create-blog", blogHandler.CreateBlogHandler).Methods("POST", "OPTIONS")
	r.HandleFunc("/blogs/user", blogHandler.GetUserBlogsHandler).Methods("GET", "OPTIONS")
	r.HandleFunc("/blogs/{blog_id}/comments", blogHandler.CreateCommentHandler).Methods("POST", "OPTIONS")
	r.PathPrefix("/blogs/uploads/").HandlerFunc(blogHandler.DownloadImageHandler).Methods("GET")

	// Fallback na gRPC-Gateway rute
	r.PathPrefix("/").Handler(gwmux)

	// Omotaj u CORS middleware
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
