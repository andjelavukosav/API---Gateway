package main

import (
	"context"
	"encoding/json"
	"example/gateway/config"
	"example/gateway/proto/stakeholders"
	"example/gateway/proto/tours"
	pb "example/gateway/proto/tours" // Proveri da li je putanja tačna
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/google/uuid"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// toursClient mora biti globalan ili dostupan handler-u
var toursClient pb.ToursServiceClient

func main() {
	cfg := config.GetConfig()

	// -------- Stakeholders gRPC connection --------
	stakeholdersConn, err := grpc.DialContext(
		context.Background(),
		cfg.StakeholdersServiceAddress,
		grpc.WithBlock(),
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
		grpc.WithBlock(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalln("Failed to dial Tours server:", err)
	}
	defer toursConn.Close()

	// Inicijalizacija gRPC klijenta za Tours servis
	toursClient = pb.NewToursServiceClient(toursConn)

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

	// -------- Standardni Go HTTP multiplekser za ručne rute --------
	mux := http.NewServeMux()

	// Povezivanje gRPC-Gateway-a sa osnovnim multiplekserom
	mux.Handle("/", gwmux)

	// Ručno registrovanje rute za upload slike ključne tačke
	mux.HandleFunc("/tours/add-keypoint", addKeyPointHandler) // Dodaj ovu liniju

	// -------- CORS handler --------
	muxWithCORS := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "http://localhost:4200")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PATCH")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		// Sada koristimo novi multiplekser koji sadrži sve rute
		mux.ServeHTTP(w, r)
	})

	mux.Handle("/uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir("/app/uploads"))))

	// -------- HTTP server --------
	gwServer := &http.Server{
		Addr:    cfg.Address,
		Handler: muxWithCORS,
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

// Handler funkcija za dodavanje ključne tačke
func addKeyPointHandler(w http.ResponseWriter, r *http.Request) {
	// 1. Postavljanje limita za veličinu fajla (10 MB)
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "Greška pri parsiranju forme: "+err.Error(), http.StatusBadRequest)
		return
	}

	// 2. Preuzimanje fajla
	file, handler, err := r.FormFile("file")
	if err != nil {
		log.Println("FormFile error:", err) // <--- ovo će ti reći ako fajl uopšte ne stiže
		http.Error(w, "Greška pri preuzimanju fajla: "+err.Error(), http.StatusBadRequest)
		return
	}

	fmt.Println("Primljen fajl:", handler.Filename, "velicina:", handler.Size)
	defer file.Close()

	// 3. Generisanje jedinstvenog imena fajla
	fileExtension := filepath.Ext(handler.Filename)
	fileName := uuid.New().String() + fileExtension

	// 4. Absolutna putanja na L: disku
	uploadDir := "/app/uploads"
	// Kreiranje foldera ako ne postoji
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		http.Error(w, "Greška pri kreiranju foldera: "+err.Error(), http.StatusInternalServerError)
		return
	}

	filePath := filepath.Join(uploadDir, fileName)

	// 5. Kreiranje fajla i kopiranje sadržaja
	dst, err := os.Create(filePath)
	if err != nil {
		http.Error(w, "Greška pri kreiranju fajla: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	n, err := io.Copy(dst, file)
	fmt.Println("Bytes copied:", n, "err:", err)
	if err != nil {
		http.Error(w, "Greška pri kopiranju fajla: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if n == 0 {
		fmt.Println("Upozorenje: fajl je prazan")
	}

	// 6. Preuzimanje ostalih podataka iz forme
	tourId := r.FormValue("tourId")
	name := r.FormValue("name")
	description := r.FormValue("description")

	latitude, err := strconv.ParseFloat(r.FormValue("latitude"), 64)
	if err != nil {
		http.Error(w, "Greška pri konverziji latitude: "+err.Error(), http.StatusBadRequest)
		return
	}
	longitude, err := strconv.ParseFloat(r.FormValue("longitude"), 64)
	if err != nil {
		http.Error(w, "Greška pri konverziji longitude: "+err.Error(), http.StatusBadRequest)
		return
	}

	// 7. Formiranje putanje koja će se čuvati u bazi (relativna ili apsolutna, po dogovoru)
	imageURL := fmt.Sprintf(`http://localhost:8080/uploads/%s`, fileName)

	// 8. Kreiranje gRPC poruke
	req := &pb.AddKeyPointRequest{
		TourId: tourId,
		Point: &pb.KeyPoint{
			Name:        name,
			Description: description,
			Latitude:    latitude,
			Longitude:   longitude,
			ImageURL:    imageURL,
		},
	}

	// 9. Pozivanje gRPC servisa
	res, err := toursClient.AddKeyPoint(context.Background(), req)
	if err != nil {
		http.Error(w, "gRPC poziv nije uspeo: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 10. Slanje odgovora frontendu
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}
