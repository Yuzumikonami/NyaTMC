package cmd

import (
	"encoding"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/config"
)

var (
	configEditor  string
	configRawOnly bool
	configKeyInst string
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "查看与修改配置（改动立即生效，运行中的实例会热重载）",
	Long: `查看与修改 ~/.nyatmc/config.toml。

配置项用点分路径表示，例如：
  server.memory            全局默认内存
  daemon.watchdog          崩溃自动重启开关
  backup.include           备份清单（数组，用逗号分隔）
  server.properties.motd   写入 server.properties 的额外键

给某个实例做局部覆盖时，在末尾加上实例名（或使用 -i）：
  nyatmc config set server.port 25566 survival
  nyatmc config get server.port survival     # 查看合并后的生效值

运行中的实例会自动热重载配置，不需要重启 nyatmc。`,
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "显示配置（默认显示文件原文）",
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(app.Cfg.Path)
		if err != nil {
			return fmt.Errorf("读取配置文件失败：%w", err)
		}
		if app.JSON {
			res := app.Cfg.Resolve("")
			return app.JSONOut(map[string]any{
				"path":  app.Cfg.Path,
				"toml":  string(raw),
				"value": res,
			})
		}
		if configRawOnly {
			app.Out("%s", strings.TrimRight(string(raw), "\n"))
			return nil
		}
		app.Out("# 配置文件：%s", app.Cfg.Path)
		app.Out("%s", strings.TrimRight(string(raw), "\n"))
		return nil
	},
}

var configListCmd = &cobra.Command{
	Use:   "list [前缀]",
	Aliases: []string{"ls"},
	Short: "以 键 = 值 的形式列出所有配置项",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		prefix := ""
		if len(args) > 0 {
			prefix = strings.Trim(args[0], ".")
		}
		pairs := map[string]string{}
		walkConfig(reflect.ValueOf(app.Cfg).Elem(), "", func(path string, v reflect.Value) {
			if prefix != "" && !strings.HasPrefix(path, prefix) {
				return
			}
			if s, ok := formatValue(v); ok {
				pairs[path] = s
			}
		})
		keys := make([]string, 0, len(pairs))
		for k := range pairs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if app.JSON {
			return app.JSONOut(pairs)
		}
		for _, k := range keys {
			app.Out("%s = %s", k, pairs[k])
		}
		return nil
	},
}

var configGetCmd = &cobra.Command{
	Use:   "get <键> [实例名]",
	Short: "读取一个配置项（给出实例名时读取合并后的生效值）",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		key := args[0]
		instName := configKeyInst
		if len(args) > 1 {
			instName = args[1]
		}

		steps := splitKey(key)
		// 先按原始配置查
		if v, err := getIn(reflect.ValueOf(app.Cfg).Elem(), steps); err == nil {
			s, _ := formatValue(v)
			if app.JSON {
				return app.JSONOut(map[string]any{"key": key, "value": s, "scope": "config"})
			}
			app.Out("%s", s)
			return nil
		}

		if instName == "" {
			return fmt.Errorf("配置项 %q 不存在（用 nyatmc config list 查看全部）", key)
		}
		if !app.Cfg.HasInstance(instName) {
			return fmt.Errorf("实例 %q 不存在", instName)
		}
		res := app.Cfg.Resolve(instName)
		v, err := getIn(reflect.ValueOf(res), steps)
		if err != nil {
			return fmt.Errorf("配置项 %q 在实例 %s 的生效配置里不存在", key, instName)
		}
		s, _ := formatValue(v)
		if app.JSON {
			return app.JSONOut(map[string]any{"key": key, "value": s, "scope": "resolved", "instance": instName})
		}
		app.Out("%s", s)
		return nil
	},
}

var configSetCmd = &cobra.Command{
	Use:   "set <键> <值> [实例名]",
	Short: "修改一个配置项（给出实例名时只覆盖该实例）",
	Args:  cobra.RangeArgs(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		key, value := args[0], args[1]
		instName := configKeyInst
		if len(args) > 2 {
			instName = args[2]
		}

		steps := splitKey(key)
		scope := "全局"
		if instName != "" {
			if !app.Cfg.HasInstance(instName) {
				return fmt.Errorf("实例 %q 不存在；先用 nyatmc create %s 创建", instName, instName)
			}
			steps = append([]string{"instances", instName}, steps...)
			scope = fmt.Sprintf("实例 %s", instName)
		}

		if err := setIn(reflect.ValueOf(app.Cfg).Elem(), steps, value); err != nil {
			return err
		}
		if err := app.Cfg.Save(); err != nil {
			return err
		}
		// 配置改完立刻校验，避免写坏文件后才发现
		if vErr := app.Cfg.Validate(); vErr != nil {
			app.Warn("已写入，但配置校验未通过：%v", vErr)
		}

		shown := value
		if v, gerr := getIn(reflect.ValueOf(app.Cfg).Elem(), steps); gerr == nil {
			if s, ok := formatValue(v); ok {
				shown = s
			}
		}
		if app.JSON {
			return app.JSONOut(map[string]any{"key": strings.Join(steps, "."), "value": shown, "scope": scope})
		}
		app.OK("%s：%s = %s", scope, key, shown)
		app.Out("  运行中的实例会自动热重载；nyatmc status 可确认状态")
		return nil
	},
}

var configUnsetCmd = &cobra.Command{
	Use:   "unset <键> [实例名]",
	Short: "把配置项恢复为默认（实例覆盖则改为继承全局值）",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		key := args[0]
		instName := configKeyInst
		if len(args) > 1 {
			instName = args[1]
		}
		steps := splitKey(key)
		if instName != "" {
			if !app.Cfg.HasInstance(instName) {
				return fmt.Errorf("实例 %q 不存在", instName)
			}
			steps = append([]string{"instances", instName}, steps...)
		}
		if err := unsetIn(reflect.ValueOf(app.Cfg).Elem(), steps); err != nil {
			return err
		}
		if err := app.Cfg.Save(); err != nil {
			return err
		}
		if app.JSON {
			return app.JSONOut(map[string]any{"key": key, "unset": true})
		}
		app.OK("已恢复默认：%s", key)
		return nil
	},
}

var configPathCmd = &cobra.Command{
	Use:   "path",
	Short: "打印配置文件路径",
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		if app.JSON {
			return app.JSONOut(map[string]any{"path": app.Cfg.Path, "home": config.Home()})
		}
		app.Out("%s", app.Cfg.Path)
		return nil
	},
}

var configValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "校验配置文件",
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		warnings := app.Cfg.Warnings()
		if err := app.Cfg.Validate(); err != nil {
			if app.JSON {
				_ = app.JSONOut(map[string]any{"ok": false, "error": err.Error(), "warnings": warnings})
			} else {
				app.Fail("配置有问题：%v", err)
				for _, w := range warnings {
					app.Warn("%s", w)
				}
			}
			return ExitWith(1, "配置校验失败")
		}
		if app.JSON {
			return app.JSONOut(map[string]any{"ok": true, "warnings": warnings, "instances": app.Cfg.InstanceNames()})
		}
		app.OK("配置校验通过：%s", app.Cfg.Path)
		for _, w := range warnings {
			app.Warn("%s", w)
		}
		return nil
	},
}

var configEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "用编辑器打开配置文件（保存后自动校验）",
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		editor := configEditor
		if editor == "" {
			editor = os.Getenv("VISUAL")
		}
		if editor == "" {
			editor = os.Getenv("EDITOR")
		}
		if editor == "" {
			if _, err := exec.LookPath("nano"); err == nil {
				editor = "nano"
			} else if _, err := exec.LookPath("vi"); err == nil {
				editor = "vi"
			}
		}
		if editor == "" {
			app.Warn("没有找到编辑器：请设置 $EDITOR，或用 nyatmc config set 修改")
			app.Out("文件路径：%s", app.Cfg.Path)
			return nil
		}

		parts := strings.Fields(editor)
		parts = append(parts, app.Cfg.Path)
		c := exec.Command(parts[0], parts[1:]...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("编辑器退出异常：%w", err)
		}

		reloaded, err := config.Load(app.Cfg.Path)
		if err != nil {
			app.Fail("保存后的配置无法解析：%v", err)
			return ExitWith(1, "配置文件无效")
		}
		if err := reloaded.Validate(); err != nil {
			app.Fail("配置校验失败：%v", err)
			return ExitWith(1, "配置校验失败")
		}
		app.OK("配置已更新并通过校验（运行中的实例会自动热重载）")
		for _, w := range reloaded.Warnings() {
			app.Warn("%s", w)
		}
		return nil
	},
}

// ---------------------------------------------------------------- 反射工具

func splitKey(key string) []string {
	key = strings.Trim(strings.TrimSpace(key), ".")
	if key == "" {
		return nil
	}
	parts := strings.Split(key, ".")
	out := parts[:0]
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, strings.TrimSpace(p))
		}
	}
	return out
}

func tomlName(f reflect.StructField) string {
	tag := f.Tag.Get("toml")
	if tag == "" || tag == "-" {
		return ""
	}
	return strings.Split(tag, ",")[0]
}

func fieldByName(v reflect.Value, name string) (reflect.Value, error) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if tomlName(t.Field(i)) == name {
			return v.Field(i), nil
		}
	}
	// 给出可用的候选名，帮助用户纠正拼写
	var names []string
	for i := 0; i < t.NumField(); i++ {
		if n := tomlName(t.Field(i)); n != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return reflect.Value{}, fmt.Errorf("未知配置项 %q（可用：%s）", name, strings.Join(names, "、"))
}

// setIn 沿点分路径写入一个标量值；指针会自动分配，字符串 map 会自动建键。
func setIn(v reflect.Value, steps []string, raw string) error {
	if len(steps) == 0 {
		return fmt.Errorf("配置项路径不完整")
	}
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			if !v.CanSet() {
				return fmt.Errorf("无法修改该配置项")
			}
			v.Set(reflect.New(v.Type().Elem()))
		}
		v = v.Elem()
	}

	switch v.Kind() {
	case reflect.Struct:
		f, err := fieldByName(v, steps[0])
		if err != nil {
			return err
		}
		if len(steps) == 1 {
			return assignScalar(f, raw)
		}
		return setIn(f, steps[1:], raw)

	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("不支持修改这种配置结构")
		}
		kv := reflect.ValueOf(steps[0]).Convert(v.Type().Key())
		et := v.Type().Elem()
		if len(steps) == 1 {
			nv := reflect.New(et).Elem()
			if err := assignScalar(nv, raw); err != nil {
				return err
			}
			if v.IsNil() {
				if !v.CanSet() {
					return fmt.Errorf("无法修改该配置项")
				}
				v.Set(reflect.MakeMap(v.Type()))
			}
			v.SetMapIndex(kv, nv)
			return nil
		}
		elem := v.MapIndex(kv)
		if !elem.IsValid() {
			if et.Kind() != reflect.Struct {
				return fmt.Errorf("配置项 %q 不存在", steps[0])
			}
			elem = reflect.New(et).Elem()
		}
		cp := reflect.New(et).Elem()
		cp.Set(elem)
		if err := setIn(cp, steps[1:], raw); err != nil {
			return err
		}
		if v.IsNil() {
			if !v.CanSet() {
				return fmt.Errorf("无法修改该配置项")
			}
			v.Set(reflect.MakeMap(v.Type()))
		}
		v.SetMapIndex(kv, cp)
		return nil

	default:
		return fmt.Errorf("配置项 %q 不是可展开的节点（当前类型 %s）", steps[0], v.Kind())
	}
}

// unsetIn 把配置项恢复为零值（实例覆盖即变成“继承全局”），map 键则直接删除。
func unsetIn(v reflect.Value, steps []string) error {
	if len(steps) == 0 {
		return fmt.Errorf("配置项路径不完整")
	}
	switch v.Kind() {
	case reflect.Ptr:
		if v.IsNil() {
			return nil
		}
		if len(steps) == 0 {
			return nil
		}
		return unsetIn(v.Elem(), steps)
	case reflect.Struct:
		f, err := fieldByName(v, steps[0])
		if err != nil {
			return err
		}
		if len(steps) == 1 {
			if !f.CanSet() {
				return fmt.Errorf("无法修改该配置项")
			}
			f.Set(reflect.Zero(f.Type()))
			return nil
		}
		return unsetIn(f, steps[1:])
	case reflect.Map:
		if len(steps) == 1 {
			if v.IsNil() {
				return nil
			}
			v.SetMapIndex(reflect.ValueOf(steps[0]).Convert(v.Type().Key()), reflect.Value{})
			return nil
		}
		elem := v.MapIndex(reflect.ValueOf(steps[0]).Convert(v.Type().Key()))
		if !elem.IsValid() {
			return fmt.Errorf("配置项 %q 不存在", steps[0])
		}
		cp := reflect.New(v.Type().Elem()).Elem()
		cp.Set(elem)
		if err := unsetIn(cp, steps[1:]); err != nil {
			return err
		}
		v.SetMapIndex(reflect.ValueOf(steps[0]).Convert(v.Type().Key()), cp)
		return nil
	default:
		return fmt.Errorf("配置项 %q 不是可展开的节点", steps[0])
	}
}

// assignScalar 把命令行字符串写进一个标量字段。
func assignScalar(v reflect.Value, raw string) error {
	raw = strings.TrimSpace(raw)

	// Duration / Size 等自定义类型通过 encoding.TextUnmarshaler 解析
	if v.CanAddr() {
		if u, ok := v.Addr().Interface().(encoding.TextUnmarshaler); ok {
			if err := u.UnmarshalText([]byte(raw)); err != nil {
				return fmt.Errorf("值 %q 无法解析：%w", raw, err)
			}
			return nil
		}
	}
	if u, ok := v.Interface().(encoding.TextUnmarshaler); ok {
		if err := u.UnmarshalText([]byte(raw)); err != nil {
			return fmt.Errorf("值 %q 无法解析：%w", raw, err)
		}
		return nil
	}

	switch v.Kind() {
	case reflect.Ptr:
		nv := reflect.New(v.Type().Elem())
		if err := assignScalar(nv.Elem(), raw); err != nil {
			return err
		}
		v.Set(nv)
		return nil
	case reflect.String:
		v.SetString(raw)
		return nil
	case reflect.Bool:
		b, err := parseBoolValue(raw)
		if err != nil {
			return err
		}
		v.SetBool(b)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("值 %q 不是整数", raw)
		}
		if v.OverflowInt(n) {
			return fmt.Errorf("值 %d 超出 %s 的范围", n, v.Type())
		}
		v.SetInt(n)
		return nil
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("值 %q 不是数字", raw)
		}
		v.SetFloat(f)
		return nil
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("暂不支持直接修改 %s 类型的数组，请用 nyatmc config edit", v.Type())
		}
		items := parseListValue(raw)
		out := reflect.MakeSlice(v.Type(), 0, len(items))
		for _, it := range items {
			out = reflect.Append(out, reflect.ValueOf(it).Convert(v.Type().Elem()))
		}
		v.Set(out)
		return nil
	default:
		return fmt.Errorf("暂不支持直接修改 %s 类型，请用 nyatmc config edit", v.Type())
	}
}

func parseBoolValue(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "t", "yes", "y", "1", "on", "开", "是":
		return true, nil
	case "false", "f", "no", "n", "0", "off", "关", "否":
		return false, nil
	default:
		return false, fmt.Errorf("值 %q 不是布尔（用 true/false）", raw)
	}
}

func parseListValue(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}
	}
	if raw == "[]" {
		return []string{}
	}
	raw = strings.TrimPrefix(strings.TrimSuffix(raw, "]"), "[")
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(strings.TrimSpace(f), `"'`)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// getIn 沿点分路径读出一个值。
func getIn(v reflect.Value, steps []string) (reflect.Value, error) {
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return reflect.Value{}, fmt.Errorf("配置项未设置")
		}
		v = v.Elem()
	}
	if len(steps) == 0 {
		return v, nil
	}
	switch v.Kind() {
	case reflect.Struct:
		f, err := fieldByName(v, steps[0])
		if err != nil {
			return reflect.Value{}, err
		}
		return getIn(f, steps[1:])
	case reflect.Map:
		elem := v.MapIndex(reflect.ValueOf(steps[0]).Convert(v.Type().Key()))
		if !elem.IsValid() {
			return reflect.Value{}, fmt.Errorf("配置项 %q 不存在", steps[0])
		}
		return getIn(elem, steps[1:])
	default:
		return reflect.Value{}, fmt.Errorf("配置项 %q 不是可展开的节点", steps[0])
	}
}

// formatValue 把配置值格式化成命令行友好的字符串。
func formatValue(v reflect.Value) (string, bool) {
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return "", true
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return "", false
	}
	if v.CanInterface() {
		if m, ok := v.Interface().(encoding.TextMarshaler); ok {
			if b, err := m.MarshalText(); err == nil {
				return string(b), true
			}
		}
	}
	switch v.Kind() {
	case reflect.String:
		return v.String(), true
	case reflect.Bool:
		return strconv.FormatBool(v.Bool()), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), true
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64), true
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			return "", false
		}
		items := make([]string, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			items = append(items, v.Index(i).String())
		}
		return strings.Join(items, ","), true
	default:
		return "", false
	}
}

// walkConfig 深度遍历配置，对每个叶子调用 emit（用于 config list）。
func walkConfig(v reflect.Value, prefix string, emit func(path string, v reflect.Value)) {
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			name := tomlName(t.Field(i))
			if name == "" {
				continue
			}
			walkConfig(v.Field(i), joinPath(prefix, name), emit)
		}
	case reflect.Map:
		if v.IsNil() {
			return
		}
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		for _, k := range keys {
			walkConfig(v.MapIndex(k), joinPath(prefix, k.String()), emit)
		}
	default:
		if prefix != "" {
			emit(prefix, v)
		}
	}
}

func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// marshalJSON 便于调试输出。
func marshalJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}

var _ = marshalJSON

func init() {
	configCmd.PersistentFlags().StringVar(&configKeyInst, "for-instance", "", "把操作限定到某个实例（等价于在命令末尾写实例名）")
	configShowCmd.Flags().BoolVar(&configRawOnly, "raw", true, "只输出 TOML 原文")
	configEditCmd.Flags().StringVar(&configEditor, "editor", "", "指定编辑器（默认读 $VISUAL/$EDITOR）")

	configCmd.AddCommand(configShowCmd, configListCmd, configGetCmd, configSetCmd, configUnsetCmd, configPathCmd, configValidateCmd, configEditCmd)
	rootCmd.AddCommand(configCmd)
}
