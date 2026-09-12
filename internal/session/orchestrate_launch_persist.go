package session

import "encoding/json"

const toolDataOrchestrateLaunchKey = "orchestrate_launch"

func WriteOrchestrateLaunchToToolData(td json.RawMessage, launch *ResolvedLaunch) json.RawMessage {
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(td, &m)
	if launch == nil {
		delete(m, toolDataOrchestrateLaunchKey)
	} else if raw, err := json.Marshal(launch); err == nil {
		m[toolDataOrchestrateLaunchKey] = raw
	}
	out, _ := json.Marshal(m)
	return out
}

func ReadOrchestrateLaunchFromToolData(td json.RawMessage) *ResolvedLaunch {
	var blob struct {
		Launch *ResolvedLaunch `json:"orchestrate_launch"`
	}
	if json.Unmarshal(td, &blob) != nil {
		return nil
	}
	return blob.Launch
}
