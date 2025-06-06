package image

import (
	"context"
	"fmt"

	"contrib.rocks/apps/api/go/dataurl"
	"contrib.rocks/apps/api/go/model"
	"contrib.rocks/apps/api/go/renderer"
	"contrib.rocks/apps/api/go/util"
	"contrib.rocks/apps/api/internal/logger"
	"contrib.rocks/apps/api/internal/service/internal/appcache"
	"contrib.rocks/apps/api/internal/service/internal/cachekey"
	"contrib.rocks/apps/api/internal/service/internal/cacheutil"
	"contrib.rocks/apps/api/internal/tracing"
	"golang.org/x/sync/errgroup"
)

// DataURLConverterFunc defines the function signature for converting URLs to data URLs
type DataURLConverterFunc func(ctx context.Context, remoteURL string, extraParams map[string]string) (string, error)

// Default implementation uses the dataurl package
var dataURLConverter DataURLConverterFunc = dataurl.Convert

func New(cache appcache.AppCache) *Service {
	return &Service{cache}
}

type Service struct {
	cache appcache.AppCache
}

func (s *Service) GetImage(c context.Context, repo *model.Repository, options *renderer.RendererOptions, includeAnonymous bool) (model.FileHandle, error) {
	ctx, span := tracing.Tracer().Start(c, "image.Service.GetImage")
	defer span.End()
	log := logger.LoggerFromContext(ctx)

	options = normalizeRendererOptions(options)
	cacheKey := cachekey.ForImage(repo, options, "svg", includeAnonymous)

	cache, err := s.cache.Get(ctx, cacheKey)
	if err != nil {
		return nil, err
	}
	if cache == nil {
		cacheutil.LogCacheMiss(ctx, "image", cacheKey)
		return nil, nil
	}
	log.Debug(fmt.Sprintf("restored image from cache: %s", cacheKey))
	return cache, nil
}

func (s *Service) RenderImage(c context.Context, data *model.RepositoryContributors, options *renderer.RendererOptions, includeAnonymous bool) (model.FileHandle, error) {
	ctx, span := tracing.Tracer().Start(c, "image.Service.RenderImage")
	defer span.End()

	options = normalizeRendererOptions(options)
	cacheKey := cachekey.ForImage(data.Repository, options, "svg", includeAnonymous)

	data, err := s.normalizeContributors(ctx, data, options, includeAnonymous)
	if err != nil {
		return nil, err
	}

	image := renderer.NewRenderer(options).Render(data)

	err = s.cache.Save(c, cacheKey, image.Bytes(), image.ContentType())
	if err != nil {
		return nil, err
	}
	return image, nil
}

func (s *Service) normalizeContributors(ctx context.Context, base *model.RepositoryContributors, options *renderer.RendererOptions, includeAnonymous bool) (*model.RepositoryContributors, error) {
	// フィルタリングと制限
	filteredContributors := make([]*model.Contributor, 0, len(base.Contributors))
	for _, c := range base.Contributors {
		if includeAnonymous || c.ID != 0 {
			filteredContributors = append(filteredContributors, c)
		}
	}

	maxCount := util.Min(options.MaxCount, len(filteredContributors))
	result := &model.RepositoryContributors{
		Repository:      base.Repository,
		StargazersCount: base.StargazersCount,
		Contributors:    make([]*model.Contributor, maxCount),
	}
	copy(result.Contributors, filteredContributors[:maxCount])

	// サイズパラメータの共通設定
	sizeParams := map[string]string{
		"size": fmt.Sprint(options.ItemSize),
		"s":    fmt.Sprint(options.ItemSize),
	}

	// データURL変換の並列処理
	eg, ctx := errgroup.WithContext(ctx)
	for i := range result.Contributors {
		i := i // ループ変数をキャプチャ
		eg.Go(func() error {
			contributor := result.Contributors[i]
			dataURL, err := dataURLConverter(ctx, contributor.AvatarURL, sizeParams)
			if err != nil {
				return fmt.Errorf("avatar URL conversion failed for contributor %s: %w",
					contributor.Login, err)
			}
			result.Contributors[i].AvatarURL = dataURL
			return nil
		})
	}

	return result, eg.Wait()
}
