package client

import "encoding/json"

// UnmarshalJSON records optional feature presence without changing existing Go field types.
func (l *Light) UnmarshalJSON(data []byte) error {
	type plain Light
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*l = Light(decoded)
	raw, ok := fields["dimming"]
	l.HasDimming = ok && string(raw) != "null"
	return nil
}

// MarshalJSON sends writable light fields without read-only capability metadata.
func (u LightUpdate) MarshalJSON() ([]byte, error) {
	fields := map[string]interface{}{}
	if u.On != nil {
		fields["on"] = u.On
	}
	if u.Dimming != nil {
		fields["dimming"] = map[string]float64{"brightness": u.Dimming.Brightness}
	}
	if u.Color != nil {
		fields["color"] = map[string]XY{"xy": u.Color.XY}
	}
	if u.ColorTemperature != nil {
		fields["color_temperature"] = map[string]int{"mirek": u.ColorTemperature.Mirek}
	}
	if u.Dynamics != nil {
		fields["dynamics"] = u.Dynamics
	}
	if u.Effects != nil {
		fields["effects"] = map[string]string{"effect": u.Effects.Effect}
	}
	if u.Alert != nil {
		fields["alert"] = map[string]string{"action": u.Alert.Action}
	}
	return json.Marshal(fields)
}
