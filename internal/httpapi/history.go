package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bnhminh1010/homelab-dashboard/internal/history"
	"github.com/bnhminh1010/homelab-dashboard/internal/store"
	"github.com/gin-gonic/gin"
)

const maxHistoryRange = 90 * 24 * time.Hour

const (
	defaultHistoryMaxPoints     = 600
	maximumHistoryMaxPoints     = 1000
	maximumHistoryNodeBytes     = 128
	maximumHistoryResourceBytes = 256
)

func (s *Server) getHistoryResources(c *gin.Context) {
	nodeID, ok := historyNodeID(c)
	if !ok {
		return
	}
	containers, err := s.options.History.ListContainerInstances(c.Request.Context(), nodeID)
	if err != nil {
		writeHistoryError(c, err)
		return
	}
	services, err := s.options.History.ListServiceSeries(c.Request.Context(), nodeID)
	if err != nil {
		writeHistoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"nodeId": nodeID, "containers": containers, "services": services,
	})
}

func historyNodeID(c *gin.Context) (string, bool) {
	nodeID := strings.TrimSpace(c.DefaultQuery("node", history.LocalNodeID))
	if nodeID == "" {
		nodeID = history.LocalNodeID
	}
	if !validHistoryIdentifier(nodeID, maximumHistoryNodeBytes) {
		writeError(c, http.StatusBadRequest, "invalid_history_query", "The history node id is invalid.", nil)
		return "", false
	}
	return nodeID, true
}

func validHistoryIdentifier(value string, maximumBytes int) bool {
	if value == "" || len(value) > maximumBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func (s *Server) getSystemHistory(c *gin.Context) {
	query, ok := historyQuery(c, "")
	if !ok {
		return
	}
	maxPoints, ok := historyPointLimit(c)
	if !ok {
		return
	}
	query.Resolution = boundedHostResolution(query, maxPoints)
	points, resolution, err := s.options.History.QueryHostHistory(c.Request.Context(), query)
	if err != nil {
		writeHistoryError(c, err)
		return
	}
	sourcePointCount := len(points)
	points = downsampleHostPoints(points, maxPoints)
	response := gin.H{
		"nodeId": query.NodeID, "from": query.From, "to": query.To,
		"resolution": resolution, "points": points, "sourcePointCount": sourcePointCount,
	}
	if s.options.HistoryQuota != nil {
		response["quota"] = s.options.HistoryQuota()
	}
	c.JSON(http.StatusOK, response)
}

func (s *Server) getContainerHistory(c *gin.Context) {
	query, ok := historyQuery(c, c.Param("id"))
	if !ok {
		return
	}
	maxPoints, ok := historyPointLimit(c)
	if !ok {
		return
	}
	query.Resolution = boundedContainerResolution(query, maxPoints)
	instance, err := s.options.History.GetContainerInstance(c.Request.Context(), query.NodeID, query.InstanceID)
	if err != nil {
		writeHistoryError(c, err)
		return
	}
	points, resolution, err := s.options.History.QueryContainerHistory(c.Request.Context(), query)
	if err != nil {
		writeHistoryError(c, err)
		return
	}
	sourcePointCount := len(points)
	points = downsampleContainerPoints(points, maxPoints)
	response := gin.H{
		"nodeId": query.NodeID, "instance": instance, "from": query.From, "to": query.To,
		"resolution": resolution, "points": points, "sourcePointCount": sourcePointCount,
	}
	if s.options.HistoryQuota != nil {
		response["quota"] = s.options.HistoryQuota()
	}
	c.JSON(http.StatusOK, response)
}

func (s *Server) getServiceHistory(c *gin.Context) {
	query, ok := historyQuery(c, "")
	if !ok {
		return
	}
	maxPoints, ok := historyPointLimit(c)
	if !ok {
		return
	}
	serviceID := strings.TrimSpace(c.Param("id"))
	if !validHistoryIdentifier(serviceID, maximumHistoryResourceBytes) {
		writeError(c, http.StatusBadRequest, "invalid_history_query", "The history service id is invalid.", nil)
		return
	}
	points, err := s.options.History.QueryServiceUptime(c.Request.Context(), query.NodeID, serviceID, query.From, query.To)
	if err != nil {
		writeHistoryError(c, err)
		return
	}
	sourcePointCount := len(points)
	points = downsampleServicePoints(points, maxPoints)
	response := gin.H{
		"nodeId": query.NodeID, "serviceId": serviceID, "from": query.From, "to": query.To,
		"resolution": history.Resolution1h, "points": points, "sourcePointCount": sourcePointCount,
	}
	if s.options.HistoryQuota != nil {
		response["quota"] = s.options.HistoryQuota()
	}
	c.JSON(http.StatusOK, response)
}

func historyPointLimit(c *gin.Context) (int, bool) {
	value := strings.TrimSpace(c.DefaultQuery("maxPoints", strconv.Itoa(defaultHistoryMaxPoints)))
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > maximumHistoryMaxPoints {
		writeError(c, http.StatusBadRequest, "invalid_history_query", "maxPoints must be between 1 and 1000.", nil)
		return 0, false
	}
	return limit, true
}

func boundedHostResolution(query history.Query, maximum int) history.Resolution {
	span := query.To.Sub(query.From)
	minimum := history.Resolution15m
	if span/history.HostSampleInterval <= time.Duration(maximum) {
		minimum = history.ResolutionRaw
	} else if span/time.Minute <= time.Duration(maximum) {
		minimum = history.Resolution1m
	}
	ranks := map[history.Resolution]int{history.ResolutionRaw: 0, history.Resolution1m: 1, history.Resolution15m: 2}
	requested := query.Resolution
	if requested == history.ResolutionAuto {
		return minimum
	}
	requestedRank, valid := ranks[requested]
	if !valid {
		return requested
	}
	if requestedRank < ranks[minimum] {
		return minimum
	}
	return requested
}

func boundedContainerResolution(query history.Query, maximum int) history.Resolution {
	span := query.To.Sub(query.From)
	minimum := history.Resolution1h
	if span/history.ContainerSampleInterval <= time.Duration(maximum) {
		minimum = history.ResolutionRaw
	} else if span/(5*time.Minute) <= time.Duration(maximum) {
		minimum = history.Resolution5m
	}
	ranks := map[history.Resolution]int{history.ResolutionRaw: 0, history.Resolution5m: 1, history.Resolution1h: 2}
	requested := query.Resolution
	if requested == history.ResolutionAuto {
		return minimum
	}
	requestedRank, valid := ranks[requested]
	if !valid {
		return requested
	}
	if requestedRank < ranks[minimum] {
		return minimum
	}
	return requested
}

func pointBuckets(length, maximum int, visit func(int, int)) {
	if length <= maximum {
		for index := 0; index < length; index++ {
			visit(index, index+1)
		}
		return
	}
	for bucket := 0; bucket < maximum; bucket++ {
		start := bucket * length / maximum
		end := (bucket + 1) * length / maximum
		if end <= start {
			end = start + 1
		}
		visit(start, end)
	}
}

func downsampleHostPoints(points []history.HostPoint, maximum int) []history.HostPoint {
	if len(points) <= maximum {
		return points
	}
	result := make([]history.HostPoint, 0, maximum)
	pointBuckets(len(points), maximum, func(start, end int) {
		var output history.HostPoint
		var weight, temperatureWeight float64
		var temperature float64
		for _, point := range points[start:end] {
			pointWeight := float64(point.SampleCount)
			if pointWeight <= 0 {
				pointWeight = 1
			}
			weight += pointWeight
			output.SampleCount += point.SampleCount
			output.CPUPercent += point.CPUPercent * pointWeight
			output.MemoryUsedBytes += point.MemoryUsedBytes * pointWeight
			output.MemoryTotalBytes += point.MemoryTotalBytes * pointWeight
			output.DiskUsedBytes += point.DiskUsedBytes * pointWeight
			output.DiskTotalBytes += point.DiskTotalBytes * pointWeight
			output.NetworkRXBytesPerSecond += point.NetworkRXBytesPerSecond * pointWeight
			output.NetworkTXBytesPerSecond += point.NetworkTXBytesPerSecond * pointWeight
			output.LoadOne += point.LoadOne * pointWeight
			if point.TemperatureCelsius != nil {
				temperature += *point.TemperatureCelsius * pointWeight
				temperatureWeight += pointWeight
			}
		}
		output.At = points[start+(end-start)/2].At
		output.CPUPercent /= weight
		output.MemoryUsedBytes /= weight
		output.MemoryTotalBytes /= weight
		output.DiskUsedBytes /= weight
		output.DiskTotalBytes /= weight
		output.NetworkRXBytesPerSecond /= weight
		output.NetworkTXBytesPerSecond /= weight
		output.LoadOne /= weight
		if temperatureWeight > 0 {
			value := temperature / temperatureWeight
			output.TemperatureCelsius = &value
		}
		result = append(result, output)
	})
	return result
}

func downsampleContainerPoints(points []history.ContainerPoint, maximum int) []history.ContainerPoint {
	if len(points) <= maximum {
		return points
	}
	result := make([]history.ContainerPoint, 0, maximum)
	pointBuckets(len(points), maximum, func(start, end int) {
		var output history.ContainerPoint
		var weight float64
		for _, point := range points[start:end] {
			pointWeight := float64(point.SampleCount)
			if pointWeight <= 0 {
				pointWeight = 1
			}
			weight += pointWeight
			output.SampleCount += point.SampleCount
			output.CPUPercent += point.CPUPercent * pointWeight
			output.MemoryUsageBytes += point.MemoryUsageBytes * pointWeight
			output.MemoryLimitBytes += point.MemoryLimitBytes * pointWeight
		}
		output.At = points[start+(end-start)/2].At
		output.CPUPercent /= weight
		output.MemoryUsageBytes /= weight
		output.MemoryLimitBytes /= weight
		output.RestartCount = points[end-1].RestartCount
		result = append(result, output)
	})
	return result
}

func downsampleServicePoints(points []history.ServiceUptimePoint, maximum int) []history.ServiceUptimePoint {
	if len(points) <= maximum {
		return points
	}
	result := make([]history.ServiceUptimePoint, 0, maximum)
	pointBuckets(len(points), maximum, func(start, end int) {
		output := history.ServiceUptimePoint{At: points[start].At}
		for _, point := range points[start:end] {
			output.UpSeconds += point.UpSeconds
			output.DownSeconds += point.DownSeconds
			output.DegradedSeconds += point.DegradedSeconds
			output.UnknownSeconds += point.UnknownSeconds
			output.TransitionCount += point.TransitionCount
		}
		result = append(result, output)
	})
	return result
}

func historyQuery(c *gin.Context, instanceID string) (history.Query, bool) {
	nodeID, ok := historyNodeID(c)
	if !ok {
		return history.Query{}, false
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID != "" && !validHistoryIdentifier(instanceID, maximumHistoryResourceBytes) {
		writeError(c, http.StatusBadRequest, "invalid_history_query", "The history container id is invalid.", nil)
		return history.Query{}, false
	}
	now := time.Now().UTC()
	from, to := time.Time{}, now
	if value := strings.TrimSpace(c.Query("to")); value != "" {
		parsed, err := parseHistoryTime(value)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid_history_query", "History 'to' must be RFC3339 or a Unix timestamp.", nil)
			return history.Query{}, false
		}
		to = parsed
	}
	if value := strings.TrimSpace(c.Query("from")); value != "" {
		parsed, err := parseHistoryTime(value)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid_history_query", "History 'from' must be RFC3339 or a Unix timestamp.", nil)
			return history.Query{}, false
		}
		from = parsed
	} else {
		span, err := historyRange(strings.TrimSpace(c.DefaultQuery("range", "1h")))
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid_history_range", "Range must be one of 1h, 6h, 24h, 7d, 30d, or 90d.", nil)
			return history.Query{}, false
		}
		from = to.Add(-span)
	}
	if !from.Before(to) || to.Sub(from) > maxHistoryRange {
		writeError(c, http.StatusBadRequest, "invalid_history_range", "History range must be positive and no longer than 90 days.", nil)
		return history.Query{}, false
	}
	resolution := history.Resolution(strings.TrimSpace(c.DefaultQuery("resolution", string(history.ResolutionAuto))))
	return history.Query{
		NodeID: nodeID, InstanceID: instanceID, From: from, To: to, Resolution: resolution,
	}, true
}

func parseHistoryTime(value string) (time.Time, error) {
	if unix, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.Unix(unix, 0).UTC(), nil
	}
	return time.Parse(time.RFC3339, value)
}

func historyRange(value string) (time.Duration, error) {
	switch value {
	case "1h":
		return time.Hour, nil
	case "6h":
		return 6 * time.Hour, nil
	case "24h":
		return 24 * time.Hour, nil
	case "7d":
		return 7 * 24 * time.Hour, nil
	case "30d":
		return 30 * 24 * time.Hour, nil
	case "90d":
		return 90 * 24 * time.Hour, nil
	default:
		return 0, errors.New("invalid range")
	}
}

func writeHistoryError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, history.ErrInvalidRange), errors.Is(err, history.ErrInvalidResolution):
		writeError(c, http.StatusBadRequest, "invalid_history_query", "The history query is invalid.", nil)
	case errors.Is(err, store.ErrNotFound):
		writeError(c, http.StatusNotFound, "history_not_found", "No matching history resource exists.", nil)
	default:
		writeError(c, http.StatusInternalServerError, "history_unavailable", "Unable to query metric history.", nil)
	}
}
