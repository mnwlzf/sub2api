package repository

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"

	"github.com/lib/pq"
)

type usageCostMonitorTopUserRow struct {
	UserID          int64
	Email           string
	TotalActualCost float64
	Requests        int64
	Tokens          int64
}

type usageCostMonitorTopGroupRow struct {
	GroupID         int64
	GroupName       string
	Platform        string
	TotalActualCost float64
	Requests        int64
	Tokens          int64
}

type usageCostMonitorSeriesRow struct {
	BucketStart time.Time
	Bucket      string
	UserID      int64
	Email       string
	ActualCost  float64
	Requests    int64
	Tokens      int64
}

type usageCostMonitorGroupSeriesRow struct {
	BucketStart time.Time
	Bucket      string
	GroupID     int64
	GroupName   string
	Platform    string
	ActualCost  float64
	Requests    int64
	Tokens      int64
}

type usageCostMonitorModelRow struct {
	BucketStart time.Time
	Bucket      string
	UserID      int64
	Model       string
	ActualCost  float64
}

func (r *usageLogRepository) GetUsageCostMonitor(ctx context.Context, startTime, endTime time.Time, granularity, userTZ string, userID int64, limit int) (result *usagestats.UsageCostMonitorData, err error) {
	bucketGranularity := "day"
	switch granularity {
	case "minute":
		bucketGranularity = "minute"
	case "hour":
		bucketGranularity = "hour"
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 10 {
		limit = 10
	}

	topQuery := `
		SELECT
			ul.user_id,
			COALESCE(u.email, '') AS email,
			COALESCE(SUM(ul.actual_cost), 0) AS total_actual_cost,
			COUNT(*) AS requests,
			COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0) AS tokens
		FROM usage_logs ul
		LEFT JOIN users u ON u.id = ul.user_id
		WHERE ul.created_at >= $1
		  AND ul.created_at < $2
	`
	topArgs := []any{startTime, endTime}
	if userID > 0 {
		topQuery += " AND ul.user_id = $3"
		topArgs = append(topArgs, userID)
	}
	topQuery += " GROUP BY ul.user_id, u.email ORDER BY total_actual_cost DESC, ul.user_id ASC LIMIT $"
	topQuery += strconv.Itoa(len(topArgs) + 1)
	topArgs = append(topArgs, limit)

	topRows, err := r.sql.QueryContext(ctx, topQuery, topArgs...)
	if err != nil {
		return nil, err
	}
	topUserRows := make([]usageCostMonitorTopUserRow, 0)
	func() {
		defer func() {
			if closeErr := topRows.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
		}()
		for topRows.Next() {
			var row usageCostMonitorTopUserRow
			if err = topRows.Scan(&row.UserID, &row.Email, &row.TotalActualCost, &row.Requests, &row.Tokens); err != nil {
				return
			}
			topUserRows = append(topUserRows, row)
		}
		if err == nil {
			err = topRows.Err()
		}
	}()
	if err != nil {
		return nil, err
	}

	result = &usagestats.UsageCostMonitorData{
		TopUsers:    make([]usagestats.UsageCostMonitorTopUser, 0, len(topUserRows)),
		Series:      []usagestats.UsageCostMonitorPoint{},
		TopGroups:   []usagestats.UsageCostMonitorTopGroup{},
		GroupSeries: []usagestats.UsageCostMonitorGroupPoint{},
	}
	for _, row := range topUserRows {
		result.TopUsers = append(result.TopUsers, usagestats.UsageCostMonitorTopUser{
			UserID:          row.UserID,
			Email:           row.Email,
			TotalActualCost: row.TotalActualCost,
			Requests:        row.Requests,
			Tokens:          row.Tokens,
		})
	}

	bucketStarts := make([]time.Time, 0)
	for ts := startTime; ts.Before(endTime); {
		bucketStarts = append(bucketStarts, ts)
		switch bucketGranularity {
		case "minute":
			ts = ts.Add(time.Minute)
		case "hour":
			ts = ts.Add(time.Hour)
		default:
			ts = ts.AddDate(0, 0, 1)
		}
	}

	userIDs := make([]int64, 0, len(topUserRows))
	for _, row := range topUserRows {
		userIDs = append(userIDs, row.UserID)
	}

	bucketExpr := "date_trunc('day', ul.created_at)"
	bucketLabelExpr := "TO_CHAR(date_trunc('day', ul.created_at), 'YYYY-MM-DD')"
	switch bucketGranularity {
	case "minute":
		bucketExpr = "date_trunc('minute', ul.created_at)"
		bucketLabelExpr = "TO_CHAR(date_trunc('minute', ul.created_at), 'YYYY-MM-DD HH24:MI')"
	case "hour":
		bucketExpr = "date_trunc('hour', ul.created_at)"
		bucketLabelExpr = "TO_CHAR(date_trunc('hour', ul.created_at), 'YYYY-MM-DD HH24:00')"
	}

	topGroupQuery := `
		SELECT
			COALESCE(ul.group_id, 0) AS group_id,
			COALESCE(g.name, CASE WHEN ul.group_id IS NULL THEN 'No Group' ELSE CONCAT('Group #', ul.group_id::text) END) AS group_name,
			COALESCE(g.platform, '') AS platform,
			COALESCE(SUM(ul.actual_cost), 0) AS total_actual_cost,
			COUNT(*) AS requests,
			COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0) AS tokens
		FROM usage_logs ul
		LEFT JOIN groups g ON g.id = ul.group_id
		WHERE ul.created_at >= $1
		  AND ul.created_at < $2
	`
	topGroupArgs := []any{startTime, endTime}
	if userID > 0 {
		topGroupQuery += " AND ul.user_id = $3"
		topGroupArgs = append(topGroupArgs, userID)
	}
	topGroupQuery += " GROUP BY 1, 2, 3 ORDER BY total_actual_cost DESC, group_id ASC LIMIT $"
	topGroupQuery += strconv.Itoa(len(topGroupArgs) + 1)
	topGroupArgs = append(topGroupArgs, limit)

	groupRows, err := r.sql.QueryContext(ctx, topGroupQuery, topGroupArgs...)
	if err != nil {
		return nil, err
	}
	topGroupRows := make([]usageCostMonitorTopGroupRow, 0)
	func() {
		defer func() {
			if closeErr := groupRows.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
		}()
		for groupRows.Next() {
			var row usageCostMonitorTopGroupRow
			if err = groupRows.Scan(&row.GroupID, &row.GroupName, &row.Platform, &row.TotalActualCost, &row.Requests, &row.Tokens); err != nil {
				return
			}
			topGroupRows = append(topGroupRows, row)
		}
		if err == nil {
			err = groupRows.Err()
		}
	}()
	if err != nil {
		return nil, err
	}

	for _, row := range topGroupRows {
		result.TopGroups = append(result.TopGroups, usagestats.UsageCostMonitorTopGroup{
			GroupID:         row.GroupID,
			GroupName:       row.GroupName,
			Platform:        row.Platform,
			TotalActualCost: row.TotalActualCost,
			Requests:        row.Requests,
			Tokens:          row.Tokens,
		})
	}

	if len(topUserRows) > 0 {
		result.Series = make([]usagestats.UsageCostMonitorPoint, 0)

		seriesQuery := fmt.Sprintf(`
		SELECT
			%s AS bucket_start,
			%s AS bucket,
			ul.user_id,
			COALESCE(u.email, '') AS email,
			COALESCE(SUM(ul.actual_cost), 0) AS actual_cost,
			COUNT(*) AS requests,
			COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0) AS tokens
		FROM usage_logs ul
		LEFT JOIN users u ON u.id = ul.user_id
		WHERE ul.created_at >= $1
		  AND ul.created_at < $2
		  AND ul.user_id = ANY($3)
		GROUP BY 1, 2, 3, 4
		ORDER BY bucket_start ASC, actual_cost DESC, ul.user_id ASC
	`, bucketExpr, bucketLabelExpr)

		seriesRows := make([]usageCostMonitorSeriesRow, 0)
		rows, err := r.sql.QueryContext(ctx, seriesQuery, startTime, endTime, pq.Array(userIDs))
		if err != nil {
			return nil, err
		}
		func() {
			defer func() {
				if closeErr := rows.Close(); closeErr != nil && err == nil {
					err = closeErr
				}
			}()
			for rows.Next() {
				var row usageCostMonitorSeriesRow
				if err = rows.Scan(&row.BucketStart, &row.Bucket, &row.UserID, &row.Email, &row.ActualCost, &row.Requests, &row.Tokens); err != nil {
					return
				}
				seriesRows = append(seriesRows, row)
			}
			if err == nil {
				err = rows.Err()
			}
		}()
		if err != nil {
			return nil, err
		}

		modelQuery := fmt.Sprintf(`
		SELECT
			%s AS bucket_start,
			%s AS bucket,
			ul.user_id,
			ul.model,
			COALESCE(SUM(ul.actual_cost), 0) AS actual_cost
		FROM usage_logs ul
		WHERE ul.created_at >= $1
		  AND ul.created_at < $2
		  AND ul.user_id = ANY($3)
		GROUP BY 1, 2, 3, 4
		ORDER BY bucket_start ASC, actual_cost DESC, ul.user_id ASC, ul.model ASC
	`, bucketExpr, bucketLabelExpr)

		modelRows := make([]usageCostMonitorModelRow, 0)
		rows, err = r.sql.QueryContext(ctx, modelQuery, startTime, endTime, pq.Array(userIDs))
		if err != nil {
			return nil, err
		}
		func() {
			defer func() {
				if closeErr := rows.Close(); closeErr != nil && err == nil {
					err = closeErr
				}
			}()
			for rows.Next() {
				var row usageCostMonitorModelRow
				if err = rows.Scan(&row.BucketStart, &row.Bucket, &row.UserID, &row.Model, &row.ActualCost); err != nil {
					return
				}
				modelRows = append(modelRows, row)
			}
			if err == nil {
				err = rows.Err()
			}
		}()
		if err != nil {
			return nil, err
		}

		loc := timezone.Location()
		if userLoc, loadErr := time.LoadLocation(userTZ); loadErr == nil {
			loc = userLoc
		}

		modelsByBucketUser := make(map[string][]usagestats.UsageCostMonitorModelBreakdown, len(modelRows))
		for _, row := range modelRows {
			keyLabel := formatUsageMonitorBucketLabel(row.BucketStart, bucketGranularity, loc)
			key := fmt.Sprintf("%s:%d", keyLabel, row.UserID)
			modelsByBucketUser[key] = append(modelsByBucketUser[key], usagestats.UsageCostMonitorModelBreakdown{
				Model:      row.Model,
				ActualCost: row.ActualCost,
			})
		}

		seriesMap := make(map[string]usageCostMonitorSeriesRow, len(seriesRows))
		for _, row := range seriesRows {
			keyLabel := formatUsageMonitorBucketLabel(row.BucketStart, bucketGranularity, loc)
			seriesMap[fmt.Sprintf("%s:%d", keyLabel, row.UserID)] = row
		}
		for _, ts := range bucketStarts {
			bucketLabel := formatUsageMonitorBucketLabel(ts, bucketGranularity, loc)
			for _, userRow := range topUserRows {
				key := fmt.Sprintf("%s:%d", bucketLabel, userRow.UserID)
				seriesRow, ok := seriesMap[key]
				if !ok {
					result.Series = append(result.Series, usagestats.UsageCostMonitorPoint{
						Bucket:     bucketLabel,
						UserID:     userRow.UserID,
						Email:      userRow.Email,
						ActualCost: 0,
						Requests:   0,
						Tokens:     0,
						Models:     []usagestats.UsageCostMonitorModelBreakdown{},
					})
					continue
				}
				result.Series = append(result.Series, usagestats.UsageCostMonitorPoint{
					Bucket:     bucketLabel,
					UserID:     seriesRow.UserID,
					Email:      seriesRow.Email,
					ActualCost: seriesRow.ActualCost,
					Requests:   seriesRow.Requests,
					Tokens:     seriesRow.Tokens,
					Models:     modelsByBucketUser[key],
				})
			}
		}
	}

	if len(topGroupRows) > 0 {
		loc := timezone.Location()
		if userLoc, loadErr := time.LoadLocation(userTZ); loadErr == nil {
			loc = userLoc
		}

		groupIDs := make([]int64, 0, len(topGroupRows))
		for _, row := range topGroupRows {
			groupIDs = append(groupIDs, row.GroupID)
		}

		groupSeriesQuery := fmt.Sprintf(`
			SELECT
				%s AS bucket_start,
				%s AS bucket,
				COALESCE(ul.group_id, 0) AS group_id,
				COALESCE(g.name, CASE WHEN ul.group_id IS NULL THEN 'No Group' ELSE CONCAT('Group #', ul.group_id::text) END) AS group_name,
				COALESCE(g.platform, '') AS platform,
				COALESCE(SUM(ul.actual_cost), 0) AS actual_cost,
				COUNT(*) AS requests,
				COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0) AS tokens
			FROM usage_logs ul
			LEFT JOIN groups g ON g.id = ul.group_id
			WHERE ul.created_at >= $1
			  AND ul.created_at < $2
			  AND COALESCE(ul.group_id, 0) = ANY($3)
			GROUP BY 1, 2, 3, 4, 5
			ORDER BY bucket_start ASC, actual_cost DESC, group_id ASC
		`, bucketExpr, bucketLabelExpr)

		groupSeriesRows := make([]usageCostMonitorGroupSeriesRow, 0)
		rows, err := r.sql.QueryContext(ctx, groupSeriesQuery, startTime, endTime, pq.Array(groupIDs))
		if err != nil {
			return nil, err
		}
		func() {
			defer func() {
				if closeErr := rows.Close(); closeErr != nil && err == nil {
					err = closeErr
				}
			}()
			for rows.Next() {
				var row usageCostMonitorGroupSeriesRow
				if err = rows.Scan(&row.BucketStart, &row.Bucket, &row.GroupID, &row.GroupName, &row.Platform, &row.ActualCost, &row.Requests, &row.Tokens); err != nil {
					return
				}
				groupSeriesRows = append(groupSeriesRows, row)
			}
			if err == nil {
				err = rows.Err()
			}
		}()
		if err != nil {
			return nil, err
		}

		groupSeriesMap := make(map[string]usageCostMonitorGroupSeriesRow, len(groupSeriesRows))
		for _, row := range groupSeriesRows {
			keyLabel := formatUsageMonitorBucketLabel(row.BucketStart, bucketGranularity, loc)
			groupSeriesMap[fmt.Sprintf("%s:%d", keyLabel, row.GroupID)] = row
		}

		for _, ts := range bucketStarts {
			bucketLabel := formatUsageMonitorBucketLabel(ts, bucketGranularity, loc)
			for _, groupRow := range topGroupRows {
				key := fmt.Sprintf("%s:%d", bucketLabel, groupRow.GroupID)
				seriesRow, ok := groupSeriesMap[key]
				if !ok {
					result.GroupSeries = append(result.GroupSeries, usagestats.UsageCostMonitorGroupPoint{
						Bucket:     bucketLabel,
						GroupID:    groupRow.GroupID,
						GroupName:  groupRow.GroupName,
						Platform:   groupRow.Platform,
						ActualCost: 0,
						Requests:   0,
						Tokens:     0,
					})
					continue
				}
				result.GroupSeries = append(result.GroupSeries, usagestats.UsageCostMonitorGroupPoint{
					Bucket:     bucketLabel,
					GroupID:    seriesRow.GroupID,
					GroupName:  seriesRow.GroupName,
					Platform:   seriesRow.Platform,
					ActualCost: seriesRow.ActualCost,
					Requests:   seriesRow.Requests,
					Tokens:     seriesRow.Tokens,
				})
			}
		}
	}

	return result, nil
}

func formatUsageMonitorBucketLabel(ts time.Time, bucketGranularity string, loc *time.Location) string {
	localBucket := ts.In(loc)
	switch bucketGranularity {
	case "minute":
		return localBucket.Format("2006-01-02 15:04")
	case "hour":
		return localBucket.Format("2006-01-02 15:00")
	default:
		return localBucket.Format("2006-01-02")
	}
}
