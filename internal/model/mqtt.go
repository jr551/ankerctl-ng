package model

// MQTTPrintSchedule models ct=1001 print schedule updates.
type MQTTPrintSchedule struct {
	CommandType   int    `json:"commandType"`
	Progress      any    `json:"progress,omitempty"`
	PrintProgress any    `json:"printProgress,omitempty"`
	Name          string `json:"name,omitempty"`
	FileName      string `json:"fileName,omitempty"`
	Filename      string `json:"filename,omitempty"`
	FileNameAlt   string `json:"file_name,omitempty"`
	GCode         string `json:"gcode,omitempty"`
	GCodeName     string `json:"gcode_name,omitempty"`
	TotalTime     any    `json:"totalTime,omitempty"`
	Elapsed       any    `json:"elapsed,omitempty"`
	ElapsedTime   any    `json:"elapsedTime,omitempty"`
	Time          any    `json:"time,omitempty"`
	RemainTime    any    `json:"remainTime,omitempty"`
	Remaining     any    `json:"remaining,omitempty"`
	RemainingTime any    `json:"remainingTime,omitempty"`
}

// MQTTPrintSpeed models ct=1006 print speed updates.
type MQTTPrintSpeed struct {
	CommandType int `json:"commandType"`
	Value       any `json:"value,omitempty"`
	Speed       any `json:"speed,omitempty"`
}

// MQTTModelLayer models ct=1052 layer progress updates.
type MQTTModelLayer struct {
	CommandType    int `json:"commandType"`
	Value          any `json:"value,omitempty"`
	Layer          any `json:"layer,omitempty"`
	CurrentLayer   any `json:"currentLayer,omitempty"`
	RealPrintLayer any `json:"real_print_layer,omitempty"`
	TotalLayer     any `json:"totalLayer,omitempty"`
	Total          any `json:"total,omitempty"`
	TotalLayerAlt  any `json:"total_layer,omitempty"`
}

// MQTTFilamentError models ct=1085/1086 filament error notifications.
type MQTTFilamentError struct {
	CommandType int    `json:"commandType"`
	ErrorCode   string `json:"errorCode,omitempty"`
}
