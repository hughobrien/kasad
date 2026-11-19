package kasa

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// smartCall sends one SMART-protocol method over KLAP and returns "result".
func (k *KLAP) smartCall(ctx context.Context, method string) (json.RawMessage, error) {
	req, _ := json.Marshal(map[string]any{
		"method":             method,
		"request_time_milis": time.Now().UnixMilli(),
	})
	raw, err := k.Request(ctx, req)
	if err != nil {
		return nil, err
	}
	var env struct {
		ErrorCode int             `json:"error_code"`
		Result    json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	if env.ErrorCode != 0 {
		return nil, &SmartError{Method: method, Code: env.ErrorCode}
	}
	return env.Result, nil
}

// SmartError is a non-zero error_code from the device.
type SmartError struct {
	Method string
	Code   int
}

func (e *SmartError) Error() string {
	return fmt.Sprintf("%s: device error_code %d", e.Method, e.Code)
}

// Poll reads one SMART-protocol device (for example the EP25).
func (k *KLAP) Poll(ctx context.Context) (*Reading, error) {
	raw, err := k.smartCall(ctx, "get_device_info")
	if err != nil {
		return nil, err
	}
	var di struct {
		Model    string   `json:"model"`
		Nickname string   `json:"nickname"`
		DeviceOn *bool    `json:"device_on"`
		OnTime   *float64 `json:"on_time"`
		RSSI     *float64 `json:"rssi"`
	}
	if err := json.Unmarshal(raw, &di); err != nil {
		return nil, fmt.Errorf("get_device_info: %w", err)
	}
	if di.Model == "" {
		return nil, fmt.Errorf("get_device_info: no model")
	}
	out := &Reading{Model: di.Model, RSSI: di.RSSI, OnTimeSeconds: di.OnTime}
	if nick, err := base64.StdEncoding.DecodeString(di.Nickname); err == nil {
		out.Alias = string(nick)
	} else {
		out.Alias = di.Nickname
	}
	if di.DeviceOn != nil {
		out.On, out.HasState = *di.DeviceOn, true
	}

	// Energy methods are optional: a plug without a meter returns an error
	// code, which is not a failed poll.
	if raw, err := k.smartCall(ctx, "get_energy_usage"); err == nil {
		var eu struct {
			CurrentPower *float64 `json:"current_power"` // mW
		}
		if json.Unmarshal(raw, &eu) == nil {
			out.PowerWatts = scale(eu.CurrentPower, 1e-3)
		}
	} else if _, ok := err.(*SmartError); !ok {
		return nil, err
	}
	if raw, err := k.smartCall(ctx, "get_emeter_data"); err == nil {
		var em struct {
			PowerMW   *float64 `json:"power_mw"`
			VoltageMV *float64 `json:"voltage_mv"`
			CurrentMA *float64 `json:"current_ma"`
			EnergyWh  *float64 `json:"energy_wh"`
		}
		if json.Unmarshal(raw, &em) == nil {
			out.PowerWatts = pick(scale(em.PowerMW, 1e-3), out.PowerWatts)
			out.VoltageVolts = scale(em.VoltageMV, 1e-3)
			out.CurrentAmps = scale(em.CurrentMA, 1e-3)
			out.EnergyWh = em.EnergyWh
		}
	} else if _, ok := err.(*SmartError); !ok {
		return nil, err
	}
	return out, nil
}
