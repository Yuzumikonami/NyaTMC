package assets

// link.go
// 链接assets与config包 | Link assets and config

import _ "embed"

//go:embed default_config.toml
var DefaultConfig []byte
