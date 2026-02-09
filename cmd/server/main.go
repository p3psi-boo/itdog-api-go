package main

import (
	"log"
	"os"

	"itdog-api-golang/internal/httpapi"
	"itdog-api-golang/internal/itdog"
)

func main() {
	port := os.Getenv("HTTP_PORT")
	if port == "" {
		port = "3000"
	}

	svc := itdog.NewService()
	r := httpapi.NewRouter(svc)

	log.Printf("服务已启动: http://localhost:%s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatal(err)
	}
}
