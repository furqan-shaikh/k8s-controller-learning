package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

type Response struct {
	Version string `json:"version"`
	Replica string `json:"replica"`
}

func main() {
	replicaID, err := os.Hostname()
	if err != nil {
		replicaID = "unknown"
	}

	http.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		resp := Response{
			Version: "1.0.4",
			Replica: replicaID,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	fmt.Println("Server is listening on port 8089...")
	http.ListenAndServe(":8089", nil)
}
