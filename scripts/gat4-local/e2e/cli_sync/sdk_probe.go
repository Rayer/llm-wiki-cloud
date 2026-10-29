package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
)

type result struct {
	Objects []object `json:"objects"`
	Error   string   `json:"error,omitempty"`
}

type object struct {
	Name       string `json:"name"`
	Generation int64  `json:"generation"`
	Size       int64  `json:"size"`
	ReadError  string `json:"read_error,omitempty"`
	ReadBytes  int    `json:"read_bytes,omitempty"`
	ReadSHA256 string `json:"read_sha256,omitempty"`
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: sdk_probe BUCKET PREFIX")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := storage.NewClient(ctx)
	if err != nil {
		write(result{Error: err.Error()})
		return
	}
	defer client.Close()
	answer := result{Objects: []object{}}
	objects := client.Bucket(os.Args[1]).Objects(ctx, &storage.Query{Prefix: os.Args[2]})
	for {
		attrs, err := objects.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			answer.Error = err.Error()
			break
		}
		item := object{Name: attrs.Name, Generation: attrs.Generation, Size: attrs.Size}
		reader, err := client.Bucket(os.Args[1]).Object(attrs.Name).Generation(attrs.Generation).NewReader(ctx)
		if err != nil {
			item.ReadError = err.Error()
		} else {
			data, readErr := io.ReadAll(reader)
			closeErr := reader.Close()
			if readErr != nil {
				item.ReadError = readErr.Error()
			} else if closeErr != nil {
				item.ReadError = closeErr.Error()
			} else {
				digest := sha256.Sum256(data)
				item.ReadBytes = len(data)
				item.ReadSHA256 = hex.EncodeToString(digest[:])
			}
		}
		answer.Objects = append(answer.Objects, item)
	}
	write(answer)
}

func write(value result) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		fmt.Fprintln(os.Stderr, "could not encode SDK probe result")
		os.Exit(1)
	}
}
