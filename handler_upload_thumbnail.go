package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"

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
	rawImageData, _, err := r.FormFile("thumbnail")
	mediaType := r.Header.Get("Content-Type")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not get thumbnail from request", err)
		return
	}

	imageBytes, err := io.ReadAll(rawImageData)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not read thumbnail image data into bytes", err)
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
	thumbnailURL := fmt.Sprintf("data:%v;base64,%v", mediaType, base64.StdEncoding.EncodeToString(imageBytes))
	video.ThumbnailURL = &thumbnailURL
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not update video in db", err)
		return
	}
	respondWithJSON(w, http.StatusOK, video)
}
