package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

const maxVideoUploadSize = 1 << 30

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	// Check video size
	r.Body = http.MaxBytesReader(w, r.Body, maxVideoUploadSize)

	err := r.ParseMultipartForm(maxVideoUploadSize)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "videos cannot be larger than 1GB", err)
		return
	}
	// Retrieve video id
	videoID := r.PathValue("videoID")
	if len(videoID) == 0 {
		respondWithError(w, http.StatusBadRequest, "missing video id in upload", err)
		return
	}
	// Authenticate user
	bearerToken, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "missing bearer token in header", err)
		return
	}
	userID, err := auth.ValidateJWT(bearerToken, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "user is not authorized", err)
		return
	}
	// Get video from DB
	videoUUID, err := uuid.Parse(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not parse video uuid in req", err)
		return
	}
	video, err := cfg.db.GetVideo(videoUUID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "video could not be retreived from db", err)
		return
	}
	// Ensure user is authorized to upload video
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "user doesn't have access to that video", err)
		return
	}

	// Retreive the video from the form request
	uploadedVideo, fileHeader, err := r.FormFile("video")
	defer uploadedVideo.Close()
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "video could not be found in the request", err)
		return
	}
	// Validate the content type
	videoContentType := fileHeader.Header.Get("Content-Type")
	if videoContentType == "" {
		respondWithError(w, http.StatusBadRequest, "content type not found in video upload req", err)
		return
	}
	videoMimeType, _, err := mime.ParseMediaType(videoContentType)
	if videoMimeType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "we only accept mp4", err)
		return
	}
	// Write the video to temp
	tempVideoFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not create temp video file", err)
		return
	}
	defer os.Remove(tempVideoFile.Name())
	defer tempVideoFile.Close()
	_, err = io.Copy(tempVideoFile, uploadedVideo)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not copy uploaded video to temp video file", err)
		return
	}
	// Reset temp file pointer to start to read again
	_, err = tempVideoFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not reset temp file upload pointer with seek", err)
		return
	}
	videoKeyID := make([]byte, 32)
	_, err = rand.Read(videoKeyID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not generate random video key id", err)
		return
	}

	videoKeyStringID := hex.EncodeToString(videoKeyID) + ".mp4"
	s3UploadParams := &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &videoKeyStringID,
		Body:        tempVideoFile,
		ContentType: &videoMimeType,
	}
	_, err = cfg.s3client.PutObject(r.Context(), s3UploadParams)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not upload video to s3", err)
		return
	}
	newVideoURL := fmt.Sprintf("https://%v.s3.%v.amazonaws.com/%v", cfg.s3Bucket, cfg.s3Region, videoKeyStringID)
	video.VideoURL = &newVideoURL
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not update video with new url", err)
		return
	}
}
