package engine

import (
        "context"
        "math"
        "sort"
        "strings"
        "time"
)

type RegisterStats struct {
        AccountsTotal   int     `json:"accounts_total"`
        AccountsSuccess int     `json:"accounts_success"`
        AccountsImported int     `json:"accounts_imported"`
        AvgPerMin       float64 `json:"avg_per_min"`
        FastestSec      float64 `json:"fastest_sec"`
        SlowestSec      float64 `json:"slowest_sec"`
        MedianSec       float64 `json:"median_sec"`
        Last1hCount     int     `json:"last_1h_count"`
        Last24hCount    int     `json:"last_24h_count"`
        WindowMinutes   float64 `json:"window_minutes"`
}

func (s *AccountService) RegisterStats(ctx context.Context) (RegisterStats, error) {
        list, err := s.List(ctx)
        if err != nil {
                return RegisterStats{}, err
        }
        out := RegisterStats{AccountsTotal: len(list)}
        if len(list) == 0 {
                return out, nil
        }
        var times []time.Time
        success := 0
        imported := 0
        for _, a := range list {
                if strings.EqualFold(a.Status, "success") || a.Status == "" {
                        success++
                }
                if a.Imported {
                        imported++
                }
                if ts, err := time.Parse(time.RFC3339, a.CreatedAt); err == nil {
                        times = append(times, ts)
                }
        }
        out.AccountsSuccess = success
        out.AccountsImported = imported
        if len(times) == 0 {
                return out, nil
        }
        sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
        first, last := times[0], times[len(times)-1]
        windowMin := last.Sub(first).Minutes()
        if windowMin < 1 {
                windowMin = 1
        }
        out.WindowMinutes = windowMin
        out.AvgPerMin = float64(len(times)) / windowMin

        // gaps between successive registrations as "per-account duration" proxy
        gaps := make([]float64, 0, len(times)-1)
        for i := 1; i < len(times); i++ {
                g := times[i].Sub(times[i-1]).Seconds()
                if g > 0 && g < 3600 {
                        gaps = append(gaps, g)
                }
        }
        if len(gaps) > 0 {
                sort.Float64s(gaps)
                out.FastestSec = gaps[0]
                out.SlowestSec = gaps[len(gaps)-1]
                out.MedianSec = gaps[len(gaps)/2]
        } else {
                // single sample: estimate from current run elapsed not available
                out.FastestSec = 0
                out.SlowestSec = 0
        }

        now := time.Now()
        for _, t := range times {
                if now.Sub(t) <= time.Hour {
                        out.Last1hCount++
                }
                if now.Sub(t) <= 24*time.Hour {
                        out.Last24hCount++
                }
        }
        // also fold live status if engine provided via optional external enrich
        _ = math.Max
        return out, nil
}
