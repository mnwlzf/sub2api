package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

var validSlugPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

const maxPageFileSize = 1 << 20 // 1MB
const maxTutorialAssetSize = 100 << 20 // 100MB
const userTutorialSlug = "user-tutorial"
const migrationTutorialSlug = "migration-tutorial"
const defaultUserTutorialMarkdown = `# 使用教程

欢迎使用本系统。

## 快速开始

1. 登录后从左侧菜单进入所需功能。
2. 按照页面提示完成账号、分组或订阅配置。
3. 如需查看详细执行结果，请优先查看对应日志页面。

## 常见说明

- 本页面内容支持 Markdown 语法。
- 管理员可以直接在页面右上角编辑教程内容。
- 普通用户只能查看渲染后的教程内容。
`
const defaultMigrationTutorialMarkdown = `# 迁移教程

欢迎使用迁移教程。

## 迁移前准备

1. 先确认原平台的接口地址、模型名称和 API Key。
2. 在本系统中创建可用的分组、账号和 API Key。
3. 如需平滑切换，建议先在测试环境完成连通性验证。

## 常见迁移步骤

1. 将原有客户端中的 Base URL 替换为本系统提供的地址。
2. 将原有 Key 替换为本系统生成的 API Key。
3. 按需调整模型映射、分组和额度限制配置。

## 常见说明

- 本页面内容支持 Markdown 语法。
- 管理员可以直接在页面右上角编辑迁移教程内容。
- 普通用户只能查看渲染后的迁移教程内容。
`

type PageHandler struct {
	pagesDir       string
	settingService *service.SettingService
}

func NewPageHandler(dataDir string, settingService *service.SettingService) *PageHandler {
	pagesDir := filepath.Join(dataDir, "pages")
	_ = os.MkdirAll(pagesDir, 0755)
	return &PageHandler{pagesDir: pagesDir, settingService: settingService}
}

type updateTutorialContentRequest struct {
	Content string `json:"content"`
}

type tutorialAssetUploadResponse struct {
	Filename        string `json:"filename"`
	URL             string `json:"url"`
	MarkdownSnippet string `json:"markdown_snippet"`
}

type builtinTutorialConfig struct {
	Slug            string
	DefaultMarkdown string
}

var builtinTutorialConfigs = map[string]builtinTutorialConfig{
	userTutorialSlug: {
		Slug:            userTutorialSlug,
		DefaultMarkdown: defaultUserTutorialMarkdown,
	},
	migrationTutorialSlug: {
		Slug:            migrationTutorialSlug,
		DefaultMarkdown: defaultMigrationTutorialMarkdown,
	},
}

func getBuiltinTutorialConfig(slug string) (builtinTutorialConfig, bool) {
	config, ok := builtinTutorialConfigs[slug]
	return config, ok
}

func (h *PageHandler) builtinTutorialFilePath(slug string) string {
	return filepath.Join(h.pagesDir, slug+".md")
}

func (h *PageHandler) builtinTutorialAssetsDir(slug string) string {
	return filepath.Join(h.pagesDir, slug)
}

func (h *PageHandler) resolveBuiltinTutorialSlug(c *gin.Context) (string, builtinTutorialConfig, bool) {
	slug := c.Param("slug")
	if slug == "" {
		slug = userTutorialSlug
	}

	config, ok := getBuiltinTutorialConfig(slug)
	if !ok {
		return "", builtinTutorialConfig{}, false
	}
	return slug, config, true
}

// GetPageContent serves raw markdown content for a given slug.
// GET /api/v1/pages/:slug
func (h *PageHandler) GetPageContent(c *gin.Context) {
	slug := c.Param("slug")
	if !validSlugPattern.MatchString(slug) || len(slug) > 64 {
		response.BadRequest(c, "Invalid page slug")
		return
	}

	// Visibility check: slug must be configured in custom_menu_items
	// and the user must have permission based on visibility setting
	if !h.checkSlugVisibility(c, slug) {
		c.JSON(http.StatusNotFound, gin.H{"error": "page not found"})
		return
	}

	filePath := filepath.Join(h.pagesDir, slug+".md")
	cleaned := filepath.Clean(filePath)
	if !strings.HasPrefix(cleaned, filepath.Clean(h.pagesDir)) {
		response.BadRequest(c, "Invalid page slug")
		return
	}

	info, err := os.Stat(cleaned)
	if err != nil || info.IsDir() {
		c.JSON(http.StatusNotFound, gin.H{"error": "page not found"})
		return
	}
	if info.Size() > maxPageFileSize {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "page too large"})
		return
	}

	content, err := os.ReadFile(cleaned)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read page"})
		return
	}

	c.Data(http.StatusOK, "text/markdown; charset=utf-8", content)
}

// GetTutorialContent serves the built-in tutorial markdown content for authenticated users.
// GET /api/v1/tutorial/content
func (h *PageHandler) GetTutorialContent(c *gin.Context) {
	slug, config, ok := h.resolveBuiltinTutorialSlug(c)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "tutorial not found"})
		return
	}

	content, err := os.ReadFile(h.builtinTutorialFilePath(slug))
	if err != nil {
		if os.IsNotExist(err) {
			c.Data(http.StatusOK, "text/markdown; charset=utf-8", []byte(config.DefaultMarkdown))
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read tutorial"})
		return
	}

	if len(content) > maxPageFileSize {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "tutorial too large"})
		return
	}

	c.Data(http.StatusOK, "text/markdown; charset=utf-8", content)
}

// UpdateTutorialContent saves the built-in tutorial markdown content.
// PUT /api/v1/admin/tutorial/content
func (h *PageHandler) UpdateTutorialContent(c *gin.Context) {
	slug, _, ok := h.resolveBuiltinTutorialSlug(c)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "tutorial not found"})
		return
	}

	var req updateTutorialContentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	content := []byte(req.Content)
	if len(content) > maxPageFileSize {
		response.BadRequest(c, "Tutorial content too large")
		return
	}

	if err := os.MkdirAll(h.pagesDir, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to prepare tutorial directory"})
		return
	}

	if err := os.WriteFile(h.builtinTutorialFilePath(slug), content, 0644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save tutorial"})
		return
	}

	response.Success(c, gin.H{"saved": true})
}

// ServeTutorialAsset serves uploaded tutorial assets without JWT because browser media tags
// cannot attach authorization headers.
// GET /api/v1/tutorial/assets/*filename
func (h *PageHandler) ServeTutorialAsset(c *gin.Context) {
	slug, _, ok := h.resolveBuiltinTutorialSlug(c)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}

	filename := strings.TrimPrefix(c.Param("filename"), "/")
	cleaned, ok := resolvePageImagePath(h.pagesDir, h.builtinTutorialAssetsDir(slug), filename)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}

	info, err := os.Stat(cleaned)
	if err != nil || info.IsDir() {
		c.Status(http.StatusNotFound)
		return
	}

	c.File(cleaned)
}

// UploadTutorialAsset stores an uploaded image/video/file for the tutorial page.
// POST /api/v1/admin/tutorial/assets
func (h *PageHandler) UploadTutorialAsset(c *gin.Context) {
	slug, _, ok := h.resolveBuiltinTutorialSlug(c)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "tutorial not found"})
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		response.BadRequest(c, "file is required")
		return
	}
	if fileHeader.Size <= 0 {
		response.BadRequest(c, "file is empty")
		return
	}
	if fileHeader.Size > maxTutorialAssetSize {
		response.BadRequest(c, "file is too large")
		return
	}

	src, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to open uploaded file"})
		return
	}
	defer src.Close()

	if err := os.MkdirAll(h.builtinTutorialAssetsDir(slug), 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to prepare tutorial asset directory"})
		return
	}

	filename := sanitizeTutorialAssetFilename(fileHeader.Filename)
	targetPath := filepath.Join(h.builtinTutorialAssetsDir(slug), filename)
	dst, err := os.Create(targetPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create tutorial asset"})
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save tutorial asset"})
		return
	}

	assetURL := fmt.Sprintf("/api/v1/tutorials/%s/assets/%s", url.PathEscape(slug), url.PathEscape(filename))
	response.Success(c, tutorialAssetUploadResponse{
		Filename:        filename,
		URL:             assetURL,
		MarkdownSnippet: buildTutorialAssetSnippet(fileHeader.Header.Get("Content-Type"), filename, assetURL),
	})
}

// ListPages returns available page slugs.
// GET /api/v1/pages
func (h *PageHandler) ListPages(c *gin.Context) {
	entries, err := os.ReadDir(h.pagesDir)
	if err != nil {
		response.Success(c, []string{})
		return
	}

	slugs := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".md") {
			slugs = append(slugs, strings.TrimSuffix(name, ".md"))
		}
	}
	response.Success(c, slugs)
}

// ServePageImage serves images from data/pages/{slug}/ directory.
// GET /api/v1/pages/:slug/images/*filename
// No JWT required (browser img tags can't carry tokens), but visibility is checked.
func (h *PageHandler) ServePageImage(c *gin.Context) {
	slug := c.Param("slug")
	filename := c.Param("filename")
	filename = strings.TrimPrefix(filename, "/")

	if !validSlugPattern.MatchString(slug) || len(slug) > 64 {
		c.Status(http.StatusNotFound)
		return
	}

	if !h.checkImageSlugVisibility(c, slug) {
		c.Status(http.StatusNotFound)
		return
	}

	imagesDir := filepath.Join(h.pagesDir, slug)
	cleaned, ok := resolvePageImagePath(h.pagesDir, imagesDir, filename)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}

	info, err := os.Stat(cleaned)
	if err != nil || info.IsDir() {
		c.Status(http.StatusNotFound)
		return
	}

	c.File(cleaned)
}

func resolvePageImagePath(pagesDir, imagesDir, filename string) (string, bool) {
	relPath, ok := cleanPageImageRelativePath(filename)
	if !ok {
		return "", false
	}

	cleanedPagesDir := filepath.Clean(pagesDir)
	cleanedImagesDir := filepath.Clean(imagesDir)
	cleanedTarget := filepath.Clean(filepath.Join(cleanedImagesDir, relPath))
	if !isPathWithinBase(cleanedTarget, cleanedImagesDir) {
		return "", false
	}

	realPagesDir, err := filepath.EvalSymlinks(cleanedPagesDir)
	if err != nil {
		return "", false
	}
	realImagesDir, err := filepath.EvalSymlinks(cleanedImagesDir)
	if err != nil || !isPathWithinBase(realImagesDir, realPagesDir) {
		return "", false
	}
	realTarget, err := filepath.EvalSymlinks(cleanedTarget)
	if err != nil || !isPathWithinBase(realTarget, realImagesDir) {
		return "", false
	}
	return realTarget, true
}

func cleanPageImageRelativePath(filename string) (string, bool) {
	if filename == "" {
		return "", false
	}
	if strings.HasPrefix(filename, "/") {
		return "", false
	}
	decoded, err := url.PathUnescape(filename)
	if err != nil {
		return "", false
	}
	if decoded == "" || strings.HasPrefix(decoded, "/") || strings.Contains(decoded, "\\") || strings.ContainsRune(decoded, 0) {
		return "", false
	}

	parts := make([]string, 0)
	for _, part := range strings.Split(decoded, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			return "", false
		default:
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return "", false
	}

	relPath := filepath.Join(parts...)
	if filepath.IsAbs(relPath) || filepath.VolumeName(relPath) != "" {
		return "", false
	}
	return relPath, true
}

func sanitizeTutorialAssetFilename(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "." || base == "" || base == string(filepath.Separator) {
		base = "asset"
	}

	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	stem = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_' || r == '.':
			return r
		default:
			return '-'
		}
	}, stem)
	stem = strings.Trim(stem, "-_.")
	if stem == "" {
		stem = "asset"
	}
	return fmt.Sprintf("%s-%d%s", stem, time.Now().Unix(), ext)
}

func buildTutorialAssetSnippet(contentType, filename, assetURL string) string {
	lowerType := strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case strings.HasPrefix(lowerType, "image/"):
		return fmt.Sprintf("![%s](%s)", filename, assetURL)
	case strings.HasPrefix(lowerType, "video/"):
		return fmt.Sprintf("<video controls preload=\"metadata\" src=\"%s\" style=\"max-width: 100%%; border-radius: 12px;\"></video>", assetURL)
	case strings.HasPrefix(lowerType, "audio/"):
		return fmt.Sprintf("<audio controls preload=\"metadata\" src=\"%s\"></audio>", assetURL)
	default:
		return fmt.Sprintf("[%s](%s)", filename, assetURL)
	}
}

func isPathWithinBase(path, base string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// findSlugVisibility looks up the slug in custom_menu_items and returns (visibility, found).
func (h *PageHandler) findSlugVisibility(c *gin.Context, slug string) (string, bool) {
	if h.settingService == nil {
		return "", false
	}

	raw := h.settingService.GetCustomMenuItemsRaw(c.Request.Context())
	if raw == "" || raw == "[]" {
		return "", false
	}

	var items []struct {
		URL        string `json:"url"`
		PageSlug   string `json:"page_slug"`
		Visibility string `json:"visibility"`
	}
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return "", false
	}

	for _, item := range items {
		itemSlug := item.PageSlug
		if itemSlug == "" && strings.HasPrefix(item.URL, "md:") {
			itemSlug = strings.TrimPrefix(item.URL, "md:")
		}
		if itemSlug == slug {
			return item.Visibility, true
		}
	}
	return "", false
}

// checkSlugVisibility verifies the slug is configured in custom_menu_items
// and the authenticated user has permission to view it.
func (h *PageHandler) checkSlugVisibility(c *gin.Context, slug string) bool {
	visibility, found := h.findSlugVisibility(c, slug)
	if !found {
		return false
	}
	if visibility == "admin" {
		role, _ := middleware2.GetUserRoleFromContext(c)
		return role == "admin"
	}
	return true
}

// checkImageSlugVisibility checks visibility for image requests (no JWT available).
// Only allows user-visible pages; admin-only pages are blocked.
func (h *PageHandler) checkImageSlugVisibility(c *gin.Context, slug string) bool {
	visibility, found := h.findSlugVisibility(c, slug)
	if !found {
		return false
	}
	return visibility != "admin"
}

// RegisterPageRoutes registers page routes on a router group.
func RegisterPageRoutes(v1 *gin.RouterGroup, dataDir string, jwtAuth gin.HandlerFunc, adminAuth gin.HandlerFunc, settingService *service.SettingService) {
	h := NewPageHandler(dataDir, settingService)

	// Authenticated page content (JWT required + visibility check)
	pages := v1.Group("/pages")
	pages.Use(jwtAuth)
	{
		pages.GET("/:slug", h.GetPageContent)
	}

	// Images: no JWT (browser img tags can't carry tokens), visibility check in handler
	pageImages := v1.Group("/pages")
	{
		pageImages.GET("/:slug/images/*filename", h.ServePageImage)
	}

	// Admin-only: list all available pages
	adminPages := v1.Group("/pages")
	adminPages.Use(adminAuth)
	{
		adminPages.GET("", h.ListPages)
	}

	tutorial := v1.Group("/tutorial")
	tutorial.Use(jwtAuth)
	{
		tutorial.GET("/content", h.GetTutorialContent)
	}

	tutorials := v1.Group("/tutorials")
	tutorials.Use(jwtAuth)
	{
		tutorials.GET("/:slug/content", h.GetTutorialContent)
	}

	tutorialAssets := v1.Group("/tutorial")
	{
		tutorialAssets.GET("/assets/*filename", h.ServeTutorialAsset)
	}

	tutorialsAssets := v1.Group("/tutorials")
	{
		tutorialsAssets.GET("/:slug/assets/*filename", h.ServeTutorialAsset)
	}

	adminTutorial := v1.Group("/admin/tutorial")
	adminTutorial.Use(adminAuth)
	{
		adminTutorial.GET("/content", h.GetTutorialContent)
		adminTutorial.PUT("/content", h.UpdateTutorialContent)
		adminTutorial.POST("/assets", h.UploadTutorialAsset)
	}

	adminTutorials := v1.Group("/admin/tutorials")
	adminTutorials.Use(adminAuth)
	{
		adminTutorials.GET("/:slug/content", h.GetTutorialContent)
		adminTutorials.PUT("/:slug/content", h.UpdateTutorialContent)
		adminTutorials.POST("/:slug/assets", h.UploadTutorialAsset)
	}
}
