# migrate

鍩轰簬 GORM + Cobra 鐨?Go 鏁版嵁搴撹縼绉诲伐鍏凤紝鏀寔 MySQL銆丳ostgreSQL銆丼QLite銆?

## 鐗规€?

- 杩佺Щ鏂囦欢鎸夋椂闂存埑鎺掑簭锛屾敮鎸?up / down / reset / refresh / fresh
- Advisory Lock 闃叉澶氬疄渚嬪苟鍙戣縼绉诲啿绐侊紙MySQL `GET_LOCK`銆丳ostgreSQL `pg_advisory_lock`锛?
- `pending` 鍛戒护棰勮寰呮墽琛岃縼绉伙紙dry-run锛?
- 缁撴瀯鍖栨棩蹇楁帴鍙ｏ紝鍙敞鍏?zap / zerolog / slog 绛?
- Lock name 鍙厤缃紝鍚屼竴鏁版嵁搴撳椤圭洰鍏卞瓨涓嶅啿绐?
- `make migration` 鑷姩鐢熸垚杩佺Щ鏂囦欢鍜?model 鑴氭墜鏋?
- 鎵€鏈夋搷浣滆繑鍥?`error`锛屼笉鍦ㄥ簱鍐呰皟鐢?`os.Exit`

## 瀹夎

```bash
go get github.com/gtkit/migrate/v2@latest
```

## 蹇€熷紑濮?

### 1. 鍒濆鍖?

鍦ㄧ▼搴忓惎鍔ㄦ椂璋冪敤 `migrate.Setup`锛屼紶鍏?GORM 鏁版嵁搴撹繛鎺ュ拰閫夐」锛?

```go
package main

import (
    "log"

    "github.com/gtkit/migrate/v2"
    "github.com/gtkit/migrate/v2/command"
    "github.com/spf13/cobra"
    "gorm.io/driver/mysql"
    "gorm.io/gorm"
)

func main() {
    db, err := gorm.Open(mysql.Open("user:pass@tcp(127.0.0.1:3306)/mydb?parseTime=true"), &gorm.Config{})
    if err != nil {
        log.Fatal(err)
    }

    // 鍒濆鍖栬縼绉诲伐鍏?
    if err := migrate.Setup(db,
        migrate.WithProjectName("myproject"),
        migrate.WithMigrationDir("database/migrations"),
    ); err != nil {
        log.Fatal(err)
    }

    // 娉ㄥ唽鍒?cobra
    rootCmd := &cobra.Command{Use: "myapp"}
    rootCmd.AddCommand(command.Commands()...)

    if err := rootCmd.Execute(); err != nil {
        log.Fatal(err)
    }
}
```

### 2. Setup 閫夐」涓€瑙?

```go
migrate.Setup(db,
    migrate.WithProjectName("myproject"),             // 椤圭洰鍚嶇О锛岀敤浜庝唬鐮佺敓鎴?
    migrate.WithMigrationDir("database/migrations"),  // 杩佺Щ鏂囦欢鐩綍锛岄粯璁?database/migrations
    migrate.WithTimeout(10 * time.Minute),            // 杩佺Щ瓒呮椂鏃堕棿锛岄粯璁?5 鍒嗛挓
    migrate.WithLockName("myproject_migrate"),         // 杩佺Щ閿佸悕绉帮紝榛樿 migrate_lock
    migrate.WithLogger(myLogger),                     // 鑷畾涔夋棩蹇楋紝榛樿 stdout
)
```

### 3. 鍒涘缓杩佺Щ鏂囦欢

```bash
# 鍒涘缓琛?
myapp make migration create_users_table

# 淇敼琛?
myapp make migration update_users_table

# 娣诲姞瀛楁鍒拌〃
myapp make migration add_email_to_users_table

# 鍒犻櫎瀛楁
myapp make migration drop_column_avatar_from_users_table

# 鍒犻櫎绱㈠紩
myapp make migration drop_index_email_from_users_table
```

鎵ц鍚庝細鍦?`database/migrations/` 涓嬬敓鎴愬舰濡?`2026_03_17_120000_create_users_table.go` 鐨勬枃浠讹細

```go
package migrations

import (
    "gorm.io/gorm"

    "myproject/internal/models"
    "github.com/gtkit/migrate/v2/migration"
)

func init() {
    up := func(db *gorm.DB) error {
        return db.AutoMigrate(&models.User{})
    }

    down := func(db *gorm.DB) error {
        return db.Migrator().DropTable(&models.User{})
    }

    migration.Add("2026_03_17_120000_create_users_table", up, down)
}
```

`create` 鎿嶄綔浼氬悓鏃跺湪 `internal/models/` 涓嬬敓鎴?model 鏂囦欢銆?

### 4. 鎵ц杩佺Щ

```bash
# 棰勮寰呮墽琛岀殑杩佺Щ锛坉ry-run锛屼笉瀹為檯鎵ц锛?
myapp migrate pending

# 鎵ц鎵€鏈夋湭杩佺Щ鐨勬枃浠?
myapp migrate up

# 鏌ョ湅鎵€鏈夎縼绉荤姸鎬?
myapp migrate status

# 鍥炴粴鏈€鍚庝竴鎵硅縼绉?
myapp migrate down

# 鍥炴粴鎵€鏈夎縼绉?
myapp migrate reset

# 鍥炴粴鎵€鏈夊悗閲嶆柊鎵ц
myapp migrate refresh

# 鍒犻櫎鎵€鏈夎〃鍚庨噸鏂版墽琛岋紙鈿狅笍 鍗遍櫓锛屼細涓㈠け鏁版嵁锛?
myapp migrate fresh
```

### 5. 鍛戒护杈撳嚭绀轰緥

```bash
$ myapp migrate pending
Pending migrations (2):
  1. 2026_03_17_120000_create_users_table
  2. 2026_03_17_120100_create_orders_table

Run 'migrate up' to execute these migrations.
```

```bash
$ myapp migrate up
Running migrations...
[INFO]  migrating                                          file=2026_03_17_120000_create_users_table batch=1
[INFO]  migrated                                           file=2026_03_17_120000_create_users_table
[INFO]  migrating                                          file=2026_03_17_120100_create_orders_table batch=1
[INFO]  migrated                                           file=2026_03_17_120100_create_orders_table
Migrations completed.
```

```bash
$ myapp migrate status
Migration Status:
--------------------------------------------------
  2026_03_17_120000_create_users_table              Ran (batch 1)
  2026_03_17_120100_create_orders_table              Ran (batch 1)
  2026_03_18_090000_add_email_to_users_table         Pending
```

## 鑷畾涔夋棩蹇?

榛樿鏃ュ織杈撳嚭鍒?stdout銆傜敓浜х幆澧冨缓璁敞鍏ョ粨鏋勫寲鏃ュ織瀹炵幇銆?

### Logger 鎺ュ彛

```go
type Logger interface {
    Info(msg string, keysAndValues ...any)
    Warn(msg string, keysAndValues ...any)
    Error(msg string, keysAndValues ...any)
}
```

绛惧悕鍏煎 `zap.SugaredLogger`銆乣slog` 鐨?key-value 椋庢牸銆?

### 鎺ュ叆 zap

```go
type zapLogger struct {
    s *zap.SugaredLogger
}

func (l *zapLogger) Info(msg string, kv ...any)  { l.s.Infow(msg, kv...) }
func (l *zapLogger) Warn(msg string, kv ...any)  { l.s.Warnw(msg, kv...) }
func (l *zapLogger) Error(msg string, kv ...any) { l.s.Errorw(msg, kv...) }

// 浣跨敤
sugar := zap.NewProduction().Sugar()
migrate.Setup(db, migrate.WithLogger(&zapLogger{s: sugar}))
```

### 鎺ュ叆 zerolog

```go
type zerologLogger struct {
    l zerolog.Logger
}

func (z *zerologLogger) Info(msg string, kv ...any) {
    z.l.Info().Fields(kvToMap(kv)).Msg(msg)
}
func (z *zerologLogger) Warn(msg string, kv ...any) {
    z.l.Warn().Fields(kvToMap(kv)).Msg(msg)
}
func (z *zerologLogger) Error(msg string, kv ...any) {
    z.l.Error().Fields(kvToMap(kv)).Msg(msg)
}

func kvToMap(kv []any) map[string]any {
    m := make(map[string]any, len(kv)/2)
    for i := 0; i+1 < len(kv); i += 2 {
        m[fmt.Sprint(kv[i])] = kv[i+1]
    }
    return m
}
```

### 鎺ュ叆 slog锛圙o 1.21+锛?

```go
type slogLogger struct {
    l *slog.Logger
}

func (s *slogLogger) Info(msg string, kv ...any)  { s.l.Info(msg, kv...) }
func (s *slogLogger) Warn(msg string, kv ...any)  { s.l.Warn(msg, kv...) }
func (s *slogLogger) Error(msg string, kv ...any) { s.l.Error(msg, kv...) }

// slog 鐨勭鍚嶅ぉ鐒跺尮閰嶏紝鐩存帴鍖呬竴灞傚嵆鍙?
migrate.Setup(db, migrate.WithLogger(&slogLogger{l: slog.Default()}))
```

### 闈欓粯鏃ュ織锛堟祴璇曞満鏅級

```go
migrate.Setup(db, migrate.WithLogger(&migration.NopLogger{}))
```

## 澶氶」鐩叡鐢ㄦ暟鎹簱

褰撳涓湇鍔″叡浜悓涓€涓暟鎹簱鏃讹紝浣跨敤 `WithLockName` 閬垮厤杩佺Щ閿佸啿绐侊細

```go
// user-service
migrate.Setup(db, migrate.WithLockName("user_svc_migrate"))

// order-service
migrate.Setup(db, migrate.WithLockName("order_svc_migrate"))
```

涓嶅悓鐨?lock name 浼氱敓鎴愪笉鍚岀殑 advisory lock key锛屽悇椤圭洰鐨勮縼绉讳簰涓嶉樆濉炪€?

## 缂栫▼寮忚皟鐢?

闄や簡 CLI 鍛戒护锛屼篃鍙互鍦ㄤ唬鐮佷腑鐩存帴璋冪敤 Migrator锛?

```go
m := migration.NewMigrator("database/migrations", db,
    migration.WithLockName("myproject_migrate"),
    migration.WithLogger(myLogger),
)

ctx := context.Background()

// 妫€鏌ュ緟鎵ц鐨勮縼绉?
pending, err := m.Pending(ctx)

// 鎵ц杩佺Щ
err = m.Up(ctx)

// 鍥炴粴鏈€鍚庝竴鎵?
err = m.Rollback(ctx)

// 鍥炴粴鎸囧畾姝ユ暟
err = m.RollbackSteps(ctx, 3)

// 鏌ョ湅鐘舵€?
statuses, err := m.Status(ctx)
```

## 椤圭洰缁撴瀯

```
migrate/
鈹溾攢鈹€ migrate.go              # 鍏ュ彛锛歋etup銆丆onfig銆丆obra 鍛戒护
鈹溾攢鈹€ version.go
鈹溾攢鈹€ command/
鈹?  鈹斺攢鈹€ command.go          # 鑱氬悎鎵€鏈夊懡浠?
鈹溾攢鈹€ console/
鈹?  鈹斺攢鈹€ console.go          # 缁堢棰滆壊杈撳嚭
鈹溾攢鈹€ file/
鈹?  鈹斺攢鈹€ file.go             # 鏂囦欢鎿嶄綔宸ュ叿
鈹溾攢鈹€ make/
鈹?  鈹溾攢鈹€ make.go             # 浠ｇ爜鐢熸垚鏍稿績
鈹?  鈹溾攢鈹€ make_cmd.go         # make cmd 鍛戒护
鈹?  鈹溾攢鈹€ make_migration.go   # make migration 鍛戒护
鈹?  鈹溾攢鈹€ make_model.go       # make model 鍛戒护
鈹?  鈹斺攢鈹€ stubs/              # 浠ｇ爜妯℃澘
鈹斺攢鈹€ migration/
    鈹溾攢鈹€ migrator.go         # 杩佺Щ鎵ц鏍稿績
    鈹溾攢鈹€ migration_file.go   # 杩佺Щ娉ㄥ唽琛?
    鈹溾攢鈹€ model.go            # migrations 琛ㄦā鍨?
    鈹溾攢鈹€ database.go         # 澶氭暟鎹簱鏀寔
    鈹溾攢鈹€ lock.go             # 鍒嗗竷寮忚縼绉婚攣
    鈹斺攢鈹€ logger.go           # 鏃ュ織鎺ュ彛
```

## License

MIT
