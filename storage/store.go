package storage

import (
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"embed"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"

	jaegerclickhouse "github.com/jaegertracing/jaeger-clickhouse"

	"github.com/ClickHouse/clickhouse-go"
	"github.com/hashicorp/go-hclog"
	"github.com/jaegertracing/jaeger/plugin/storage/grpc/shared"
	"github.com/jaegertracing/jaeger/storage/dependencystore"
	"github.com/jaegertracing/jaeger/storage/spanstore"

	"github.com/jaegertracing/jaeger-clickhouse/storage/clickhousedependencystore"
	"github.com/jaegertracing/jaeger-clickhouse/storage/clickhousespanstore"
)

type Store struct {
	db            *sql.DB
	writer        spanstore.Writer
	reader        spanstore.Reader
	archiveWriter spanstore.Writer
	archiveReader spanstore.Reader
}

const (
	tlsConfigKey = "clickhouse_tls_config_key"
)

var (
	_ shared.StoragePlugin        = (*Store)(nil)
	_ shared.ArchiveStoragePlugin = (*Store)(nil)
	_ io.Closer                   = (*Store)(nil)
)

func NewStore(logger hclog.Logger, cfg Configuration) (*Store, error) {
	cfg.setDefaults()
	db, err := connector(cfg)
	if err != nil {
		return nil, fmt.Errorf("could not connect to database: %q", err)
	}

	if err := runInitScripts(logger, db, cfg); err != nil {
		_ = db.Close()
		return nil, err
	}
	if cfg.Replication {
		return &Store{
			db: db,
			writer: clickhousespanstore.NewSpanWriter(logger, db, cfg.SpansIndexTable, cfg.SpansTable,
				clickhousespanstore.Encoding(cfg.Encoding), cfg.BatchFlushInterval, cfg.BatchWriteSize, cfg.MaxSpanCount),
			reader: clickhousespanstore.NewTraceReader(db, cfg.OperationsTable, cfg.SpansIndexTable, cfg.SpansTable),
			archiveWriter: clickhousespanstore.NewSpanWriter(logger, db, "", cfg.GetSpansArchiveTable(),
				clickhousespanstore.Encoding(cfg.Encoding), cfg.BatchFlushInterval, cfg.BatchWriteSize, cfg.MaxSpanCount),
			archiveReader: clickhousespanstore.NewTraceReader(db, "", "", cfg.GetSpansArchiveTable()),
		}, nil
	}
	return &Store{
		db: db,
		writer: clickhousespanstore.NewSpanWriter(logger, db, cfg.SpansIndexTable, cfg.SpansTable,
			clickhousespanstore.Encoding(cfg.Encoding), cfg.BatchFlushInterval, cfg.BatchWriteSize, cfg.MaxSpanCount),
		reader: clickhousespanstore.NewTraceReader(db, cfg.OperationsTable, cfg.SpansIndexTable, cfg.SpansTable),
		archiveWriter: clickhousespanstore.NewSpanWriter(logger, db, "", cfg.GetSpansArchiveTable(),
			clickhousespanstore.Encoding(cfg.Encoding), cfg.BatchFlushInterval, cfg.BatchWriteSize, cfg.MaxSpanCount),
		archiveReader: clickhousespanstore.NewTraceReader(db, "", "", cfg.GetSpansArchiveTable()),
	}, nil
}

func connector(cfg Configuration) (*sql.DB, error) {
	params := fmt.Sprintf("%s?database=%s&username=%s&password=%s",
		cfg.Address,
		cfg.Database,
		cfg.Username,
		cfg.Password,
	)

	if cfg.CaFile != "" {
		caCert, err := ioutil.ReadFile(cfg.CaFile)
		if err != nil {
			return nil, err
		}
		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(caCert)
		err = clickhouse.RegisterTLSConfig(tlsConfigKey, &tls.Config{RootCAs: caCertPool})
		if err != nil {
			return nil, err
		}
		params += fmt.Sprintf(
			"&secure=true&tls_config=%s",
			tlsConfigKey,
		)
	}
	return clickhouseConnector(params)
}

func runInitScripts(logger hclog.Logger, db *sql.DB, cfg Configuration) error {
	var embeddedScripts embed.FS
	if cfg.Replication {
		embeddedScripts = jaegerclickhouse.EmbeddedFilesReplication
	} else {
		embeddedScripts = jaegerclickhouse.EmbeddedFilesNoReplication
	}

	var (
		sqlStatements []string
		ttlTimestamp  string
		ttlDate       string
	)
	if cfg.TTLDays > 0 {
		ttlTimestamp = fmt.Sprintf("TTL timestamp + INTERVAL %d DAY DELETE", cfg.TTLDays)
		ttlDate = fmt.Sprintf("TTL date + INTERVAL %d DAY DELETE", cfg.TTLDays)
	}
	switch {
	case cfg.InitSQLScriptsDir != "":
		filePaths, err := walkMatch(cfg.InitSQLScriptsDir, "*.sql")
		if err != nil {
			return fmt.Errorf("could not list sql files: %q", err)
		}
		sort.Strings(filePaths)
		for _, f := range filePaths {
			sqlStatement, err := ioutil.ReadFile(filepath.Clean(f))
			if err != nil {
				return err
			}
			sqlStatements = append(sqlStatements, string(sqlStatement))
		}
	case cfg.Replication:
		f, err := embeddedScripts.ReadFile("sqlscripts/replication/0001-jaeger-index-local.sql")
		if err != nil {
			return err
		}
		sqlStatements = append(sqlStatements, fmt.Sprintf(string(f), cfg.SpansIndexTable.ToLocal(), ttlTimestamp))
		f, err = embeddedScripts.ReadFile("sqlscripts/replication/0002-jaeger-spans-local.sql")
		if err != nil {
			return err
		}
		sqlStatements = append(sqlStatements, fmt.Sprintf(string(f), cfg.SpansTable.ToLocal(), ttlTimestamp))
		f, err = embeddedScripts.ReadFile("sqlscripts/replication/0003-jaeger-operations-local.sql")
		if err != nil {
			return err
		}
		sqlStatements = append(sqlStatements, fmt.Sprintf(string(f), cfg.OperationsTable.ToLocal(), ttlDate, cfg.SpansIndexTable.ToLocal().AddDbName(cfg.Database)))
		f, err = embeddedScripts.ReadFile("sqlscripts/replication/0004-jaeger-spans-archive-local.sql")
		if err != nil {
			return err
		}
		sqlStatements = append(sqlStatements, fmt.Sprintf(string(f), cfg.GetSpansArchiveTable().ToLocal(), ttlTimestamp))
		f, err = embeddedScripts.ReadFile("sqlscripts/replication/0005-distributed-city-hash.sql")
		if err != nil {
			return err
		}
		sqlStatements = append(sqlStatements, fmt.Sprintf(
			string(f),
			cfg.SpansTable,
			cfg.SpansTable.ToLocal().AddDbName(cfg.Database),
			cfg.Database,
			cfg.SpansTable.ToLocal(),
		))
		sqlStatements = append(sqlStatements, fmt.Sprintf(
			string(f),
			cfg.SpansIndexTable,
			cfg.SpansIndexTable.ToLocal().AddDbName(cfg.Database),
			cfg.Database,
			cfg.SpansIndexTable.ToLocal(),
		))
		sqlStatements = append(sqlStatements, fmt.Sprintf(
			string(f),
			cfg.GetSpansArchiveTable(),
			cfg.GetSpansArchiveTable().ToLocal().AddDbName(cfg.Database),
			cfg.Database,
			cfg.GetSpansArchiveTable().ToLocal(),
		))
		f, err = embeddedScripts.ReadFile("sqlscripts/replication/0006-distributed-rand.sql")
		if err != nil {
			return err
		}
		sqlStatements = append(sqlStatements, fmt.Sprintf(
			string(f),
			cfg.OperationsTable,
			cfg.OperationsTable.ToLocal().AddDbName(cfg.Database),
			cfg.Database,
			cfg.OperationsTable.ToLocal(),
		))
	default:
		f, err := embeddedScripts.ReadFile("sqlscripts/local/0001-jaeger-index.sql")
		if err != nil {
			return err
		}
		sqlStatements = append(sqlStatements, fmt.Sprintf(string(f), cfg.SpansIndexTable, ttlTimestamp))
		f, err = embeddedScripts.ReadFile("sqlscripts/local/0002-jaeger-spans.sql")
		if err != nil {
			return err
		}
		sqlStatements = append(sqlStatements, fmt.Sprintf(string(f), cfg.SpansTable, ttlTimestamp))
		f, err = embeddedScripts.ReadFile("sqlscripts/local/0003-jaeger-operations.sql")
		if err != nil {
			return err
		}
		sqlStatements = append(sqlStatements, fmt.Sprintf(string(f), cfg.OperationsTable, ttlDate, cfg.SpansIndexTable))
		f, err = embeddedScripts.ReadFile("sqlscripts/local/0004-jaeger-spans-archive.sql")
		if err != nil {
			return err
		}
		sqlStatements = append(sqlStatements, fmt.Sprintf(string(f), cfg.GetSpansArchiveTable(), ttlTimestamp))
	}
	return executeScripts(logger, sqlStatements, db)
}

func (s *Store) SpanReader() spanstore.Reader {
	return s.reader
}

func (s *Store) SpanWriter() spanstore.Writer {
	return s.writer
}

func (s *Store) DependencyReader() dependencystore.Reader {
	return clickhousedependencystore.NewDependencyStore()
}

func (s *Store) ArchiveSpanReader() spanstore.Reader {
	return s.archiveReader
}

func (s *Store) ArchiveSpanWriter() spanstore.Writer {
	return s.archiveWriter
}

func (s *Store) Close() error {
	return s.db.Close()
}

func clickhouseConnector(params string) (*sql.DB, error) {
	db, err := sql.Open("clickhouse", params)
	if err != nil {
		return nil, err
	}

	if err := db.Ping(); err != nil {
		return nil, err
	}

	return db, nil
}

func executeScripts(logger hclog.Logger, sqlStatements []string, db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	for _, statement := range sqlStatements {
		logger.Debug("Running SQL statement", "statement", statement)
		_, err = tx.Exec(statement)
		if err != nil {
			return fmt.Errorf("could not run sql %q: %q", statement, err)
		}
	}
	committed = true
	return tx.Commit()
}

func walkMatch(root, pattern string) ([]string, error) {
	var matches []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if matched, err := filepath.Match(pattern, filepath.Base(path)); err != nil {
			return err
		} else if matched {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return matches, nil
}
