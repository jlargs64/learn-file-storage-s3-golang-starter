package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
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

	log.Println("uploading video", videoID, "by user", userID)
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

	// Get aspect ratio
	ratio, err := getVideoAspectRatio(tempVideoFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not get aspect ratio of video", err)
		return
	}
	var videoType string
	if ratio == "16:9" {
		videoType = "landscape"
	} else if ratio == "9:16" {
		videoType = "portrait"
	} else {
		videoType = "other"
	}
	// Create a faster video
	fasterVideoPath, err := processVideoForFastStart(tempVideoFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not make faster video", err)
		return
	}
	defer os.Remove(fasterVideoPath)
	fasterFile, err := os.Open(fasterVideoPath)
	defer fasterFile.Close()
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not open faster video", err)
		return
	}
	// Upload to s3
	videoKeyStringID := videoType + "/" + hex.EncodeToString(videoKeyID) + ".mp4"
	s3UploadParams := &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &videoKeyStringID,
		Body:        fasterFile,
		ContentType: &videoMimeType,
	}
	_, err = cfg.s3client.PutObject(r.Context(), s3UploadParams)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not upload video to s3", err)
		return
	}
	newVideoURL := fmt.Sprintf("%v,%v", cfg.s3Bucket, videoKeyStringID)
	video.VideoURL = &newVideoURL

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not update video with new url", err)
		return
	}
}

type ffprobeOutput struct {
	Streams []ffprobeStream `json:"streams"`
}

type ffprobeStream struct {
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	DisplayRatio string `json:"display_aspect_ratio"`
}

func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command("ffprobe",
		"-v",
		"error",
		"-print_format",
		"json",
		"-show_streams",
		filePath,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		log.Println("there was an error when getting the aspect ratio:", string(stderr.String()))
		return "", fmt.Errorf("could not get video aspect ratio with ffmpeg: %w", err)
	}

	var streams ffprobeOutput
	err = json.Unmarshal(stdout.Bytes(), &streams)
	if err != nil {
		return "", fmt.Errorf("could not marshal ffprobe streams in video upload: %w", err)
	}

	stream := streams.Streams[0]
	if stream.DisplayRatio == "16:9" || stream.DisplayRatio == "9:16" {
		return stream.DisplayRatio, nil
	}
	return "other", nil
}

func processVideoForFastStart(filePath string) (string, error) {
	processFilePath := filePath + ".processing"
	cmd := exec.Command("ffmpeg",
		"-i",
		filePath,
		"-c",
		"copy",
		"-movflags",
		"faststart",
		"-f",
		"mp4",
		processFilePath,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		log.Println("could not create faststart copy", string(stderr.String()))
		return "", fmt.Errorf("could not create fast start: %w", err)
	}
	return processFilePath, nil
}

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	s3PresignClient := s3.NewPresignClient(s3Client)
	params := &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	}
	req, err := s3PresignClient.PresignGetObject(context.Background(),
		params,
		s3.WithPresignExpires(expireTime))
	if err != nil {
		return "", fmt.Errorf("could not get presigned http req: %w", err)
	}
	return req.URL, nil
}

func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	if video.VideoURL == nil {
		return video, nil
	}
	var bucket, key string
	urlSplits := strings.Split(*video.VideoURL, ",")
	bucket = urlSplits[0]
	key = urlSplits[1]

	url, err := generatePresignedURL(cfg.s3client, bucket, key, time.Hour*24)
	if err != nil {
		return database.Video{}, err
	}

	video.VideoURL = &url
	return video, nil
}
