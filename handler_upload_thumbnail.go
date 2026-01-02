package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

const maxMemory = 10 << 20

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	err = r.ParseMultipartForm(maxMemory)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not set max memory when parsing thumbnail uploading", err)
		return
	}
	rawImageData, fileHeader, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not get thumbnail from request", err)
		return
	}

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not retrieve video from db", err)
		return
	}
	if video.UserID != userID {
		authErr := "the user is trying to get somebody elses video"
		respondWithError(w, http.StatusUnauthorized, authErr, errors.New(authErr))
		return
	}
	// Create new thumbnail
	contentType := fileHeader.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if mediaType != "image/jpeg" && mediaType != "image/png" {
		respondWithError(w, http.StatusBadRequest, "unsupported file uploaded. only upload png or jpeg.", err)
		return
	}
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not parse mime type in thumbnail upload", err)
		return
	}
	fileEnding := strings.Split(contentType, "/")[1]
	randomVideoID := make([]byte, 32)
	_, err = rand.Read(randomVideoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not generate random video id", err)
		return
	}

	thumbnailFilePath := filepath.Join(cfg.assetsRoot, string(randomVideoID)+"."+fileEnding)
	thumbnailFile, err := os.Create(thumbnailFilePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not save file to filesystem", err)
		return
	}
	defer thumbnailFile.Close()
	_, err = io.Copy(thumbnailFile, rawImageData)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not save file to filesystem", err)
		return
	}
	thumbnailURL := fmt.Sprintf("http://localhost:%v/%v", cfg.port, thumbnailFilePath)
	video.ThumbnailURL = &thumbnailURL
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not update video in db", err)
		return
	}
	respondWithJSON(w, http.StatusOK, video)
}
