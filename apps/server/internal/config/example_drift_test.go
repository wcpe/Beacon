package config

import (
	"testing"

	beacon "github.com/wcpe/Beacon"
	"gopkg.in/yaml.v3"
)

// TestExampleYAMLMatchesBuiltinDefaults 锁定 P2-1 的根因：**样例 YAML 与内置默认不得漂移**。
//
// 为什么这条值得一个用例：样例文件才是一线部署的真源——`EnsureConfigFile` 在首启把
// `beacon.ConfigExampleYAML`（内嵌的 config.example.yml）释放为生产 config.yml，
// 而 `Default()` 只在「配置项缺失」时兜底。于是两者不一致时，**生产拿到的是样例值**，
// 内置默认被静默架空。P0 死锁事故的直接成因正是这个：`Default()` 早就是 MaxOpenConns=4，
// 但样例写着 1，生产实际拿到 1，池内没有第三条连接可破环。
//
// 只校验「Default() 里显式给出的值」：样例允许为未列的项留空（走默认），
// 也允许写更详细的注释说明，但不允许对同一项给出与内置默认**不同**的取值——
// 那会让「默认是多少」这个问题有两个答案，而生效的是样例那个。
//
// 判别力：把 config.example.yml 的 max-open-conns 改回 1，本用例立即变红并指出两值差异。
func TestExampleYAMLMatchesBuiltinDefaults(t *testing.T) {
	var sample struct {
		Database struct {
			Driver             string `yaml:"driver"`
			DSN                string `yaml:"dsn"`
			MaxOpenConns       *int   `yaml:"max-open-conns"`
			MaxIdleConns       *int   `yaml:"max-idle-conns"`
			ConnMaxLifetimeSec *int   `yaml:"conn-max-lifetime-sec"`
			CallTimeoutMs      *int   `yaml:"call-timeout-ms"`
			TxTimeoutMs        *int   `yaml:"tx-timeout-ms"`
		} `yaml:"database"`
		Archive struct {
			DSN      string `yaml:"dsn"`
			Database string `yaml:"database"`
		} `yaml:"archive"`
	}
	if err := yaml.Unmarshal(beacon.ConfigExampleYAML, &sample); err != nil {
		t.Fatalf("解析内嵌 config.example.yml 失败: %v", err)
	}

	def := Default()
	// 每项写成显式断言而不是反射遍历：反射会把「样例新增了 Default 未覆盖的项」也算通过，
	// 而这类新增恰恰是下一次漂移的入口。显式清单要求改这里的人一并想清楚默认值。
	checks := []struct {
		name        string
		sampleValue any
		defValue    any
	}{
		{"database.driver", sample.Database.Driver, def.Database.Driver},
		{"database.dsn", sample.Database.DSN, def.Database.DSN},
		{"database.max-open-conns", sample.Database.MaxOpenConns, def.Database.MaxOpenConns},
		{"database.max-idle-conns", sample.Database.MaxIdleConns, def.Database.MaxIdleConns},
		{"database.conn-max-lifetime-sec", sample.Database.ConnMaxLifetimeSec, def.Database.ConnMaxLifetimeSec},
		{"database.call-timeout-ms", sample.Database.CallTimeoutMs, def.Database.CallTimeoutMs},
		{"database.tx-timeout-ms", sample.Database.TxTimeoutMs, def.Database.TxTimeoutMs},
		{"archive.dsn", sample.Archive.DSN, def.Archive.DSN},
		{"archive.database", sample.Archive.Database, def.Archive.Database},
	}
	for _, c := range checks {
		switch sv := c.sampleValue.(type) {
		case *int:
			// 样例未列该项 → 走内置默认，无漂移风险。
			if sv == nil {
				continue
			}
			if *sv != c.defValue.(int) {
				t.Errorf("%s 漂移：样例=%d，内置默认=%d。样例才是首启释放到生产的值，"+
					"不一致会让「默认是多少」出现两个答案，且生效的是样例那个", c.name, *sv, c.defValue.(int))
			}
		case string:
			if sv != c.defValue.(string) {
				t.Errorf("%s 漂移：样例=%q，内置默认=%q", c.name, sv, c.defValue.(string))
			}
		default:
			t.Fatalf("未覆盖的断言类型 %T（新增项时请一并补上比较逻辑）", c.sampleValue)
		}
	}
}
