package system

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	updatecheckapp "github.com/chenyme/grok2api/backend/internal/application/updatecheck"
	"github.com/chenyme/grok2api/backend/internal/shared/response"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	publicAPIBaseURL func() string
	updates          *updatecheckapp.Service
}

func NewHandler(publicAPIBaseURL func() string, updates *updatecheckapp.Service) *Handler {
	if publicAPIBaseURL == nil {
		publicAPIBaseURL = func() string { return "" }
	}
	if updates == nil {
		updates = updatecheckapp.NewService("dev", nil)
	}
	return &Handler{publicAPIBaseURL: publicAPIBaseURL, updates: updates}
}

func (h *Handler) Register(router *gin.RouterGroup) {
	router.GET("/system", h.get)
	router.GET("/system/version", h.version)
	router.POST("/system/update/check", h.checkUpdate)
	router.PUT("/system/update/config", h.configureUpdate)
	router.GET("/system/update/status", h.updateStatus)
	router.POST("/system/update/install", h.installUpdate)
}

func (h *Handler) version(c *gin.Context) {
	response.Success(c, http.StatusOK, h.updates.Snapshot())
}

func (h *Handler) checkUpdate(c *gin.Context) {
	snapshot, err := h.updates.CheckLatest(c.Request.Context())
	if err != nil {
		updateError(c, err)
		return
	}
	response.Success(c, http.StatusOK, snapshot)
}

func (h *Handler) configureUpdate(c *gin.Context) {
	var input struct {
		Repository *string `json:"repository"`
	}
	if err := decodeUpdateRequest(c, &input); err != nil || input.Repository == nil {
		response.Error(c, http.StatusBadRequest, "invalidUpdateRequest", "请提供有效的 repository 字段")
		return
	}
	snapshot, err := h.updates.Configure(*input.Repository)
	if err != nil {
		updateError(c, err)
		return
	}
	response.Success(c, http.StatusOK, snapshot)
}

func (h *Handler) updateStatus(c *gin.Context) {
	job, err := h.updates.Job()
	if err != nil {
		updateError(c, err)
		return
	}
	response.Success(c, http.StatusOK, job)
}

func (h *Handler) installUpdate(c *gin.Context) {
	var input struct {
		Version string `json:"version"`
	}
	if err := decodeUpdateRequest(c, &input); err != nil || input.Version == "" {
		response.Error(c, http.StatusBadRequest, "invalidUpdateRequest", "请提供有效的 version 字段")
		return
	}
	job, err := h.updates.Install(input.Version)
	if err != nil {
		updateError(c, err)
		return
	}
	response.Success(c, http.StatusAccepted, job)
}

func decodeUpdateRequest(c *gin.Context, output any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain exactly one JSON object")
	}
	return nil
}

func updateError(c *gin.Context, err error) {
	status, code := http.StatusServiceUnavailable, "updateUnavailable"
	if errors.Is(err, updatecheckapp.ErrInvalid) {
		status, code = http.StatusBadRequest, "invalidUpdateRequest"
	}
	if errors.Is(err, updatecheckapp.ErrBusy) {
		status, code = http.StatusConflict, "updateBusy"
	}
	response.Error(c, status, code, err.Error())
}

func (h *Handler) get(c *gin.Context) {
	response.Success(c, http.StatusOK, gin.H{"publicApiBaseURL": strings.TrimRight(strings.TrimSpace(h.publicAPIBaseURL()), "/")})
}
