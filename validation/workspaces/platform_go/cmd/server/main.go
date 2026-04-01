package main

import (
	"log"
	"net/http"

	"example.com/platformgo/internal/api"
	"example.com/platformgo/internal/config"
	"example.com/platformgo/internal/service"
)

func main() {
	cfg := config.FromEnv()
	svc := service.New(cfg)
	handler := api.NewHandler(svc)
	if err := http.ListenAndServe(":8080", handler.Routes()); err != nil {
		log.Fatal(err)
	}
}
