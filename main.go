package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	pb "example/gateway/proto/blog"
	imagepb "example/gateway/proto/image"
	"example/gateway/proto/stakeholders"
	"example/gateway/handlers"
	"example/gateway/config"
	"example/gateway/middleware"

	"github.com/gorilla/mux"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	cfg := config.GetConfig()

	conn, err := grpc.DialContext(
		context.Background(),
		cfg.StakeholdersServiceAddress,
		grpc.WithBlock(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalln("Failed to dial server:", err)
	}
	defer conn.Close()

	//gwmux := runtime.NewServeMux()
	
	//Stakeholders
	client := stakeholders.NewStakeholdersServiceClient(conn)
	/*if err := stakeholders.RegisterStakeholdersServiceHandlerClient(context.Background(), gwmux, client); err != nil {
		log.Fatalln("Failed to register gateway:", err)
	}*/

	//Blog gRPC client
	blogConn, err := grpc.DialContext(
		context.Background(),
		cfg.BlogServiceAddress, 
		grpc.WithBlock(),
    	grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalln("Failed to dial BlogService:", err)
	}
	defer blogConn.Close()

	// Blog REST preko gRPC client (sve rute osim multipart create)
	blogClient := pb.NewBlogServiceClient(blogConn)
	imageClient := imagepb.NewImageServiceClient(blogConn)
	// ----- gRPC Gateway mux -----
	gwmux := runtime.NewServeMux()
	// Registracija Stakeholders servisa
	if err = stakeholders.RegisterStakeholdersServiceHandlerClient(context.Background(), gwmux, client); err != nil {
		log.Fatalln("Failed to register gateway:", err)
	}
	// Registracija Blog servisa (sve osim multipart create)
	if err = pb.RegisterBlogServiceHandlerClient(context.Background(), gwmux, blogClient); err != nil {
		log.Fatalln("Failed to register BlogService gateway:", err)
	}

	// ----- REST handler za multipart POST (create blog) -----
	blogHandler := handlers.NewBlogGatewayHandler(blogClient, imageClient)
	
	r := mux.NewRouter()
	r.HandleFunc("/blogs/create-blog", blogHandler.CreateBlogHandler).Methods("POST", "OPTIONS")
	r.HandleFunc("/blogs/user", blogHandler.GetUserBlogsHandler).Methods("GET", "OPTIONS")
	r.HandleFunc("/blogs/{blog_id}/comments", blogHandler.CreateCommentHandler).Methods("POST", "OPTIONS")
	r.PathPrefix("/blogs/uploads/").HandlerFunc(blogHandler.DownloadImageHandler).Methods("GET")
	
	// gRPC Gateway fallback za ostale rute
	r.PathPrefix("/").Handler(gwmux)
	
	// Omotaj router u CORS middleware
	handler := middleware.CORSMiddleware(r)

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

	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, syscall.SIGTERM, os.Interrupt)
	<-stopCh

	if err := gwServer.Close(); err != nil {
		log.Fatalln("error while stopping server: ", err)
	}
}

