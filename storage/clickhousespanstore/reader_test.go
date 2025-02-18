package clickhousespanstore

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jaegertracing/jaeger-clickhouse/storage/clickhousespanstore/mocks"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gogo/protobuf/proto"
	"github.com/jaegertracing/jaeger/model"
	"github.com/jaegertracing/jaeger/storage/spanstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testOperationsTable = "test_operations_table"
	testNumTraces       = 10
	testSpansInTrace    = 2
)

var testStartTime = time.Date(2010, 3, 15, 7, 40, 0, 0, time.UTC)

func TestTraceReader_FindTraceIDs(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)
	service := "service"
	start := testStartTime
	end := start.Add(24 * time.Hour)
	fullDuration := end.Sub(start)
	duration := fullDuration
	for i := 0; i < maxProgressiveSteps; i++ {
		duration /= 2
	}
	params := spanstore.TraceQueryParameters{
		ServiceName:  service,
		NumTraces:    testNumTraces,
		StartTimeMin: start,
		StartTimeMax: end,
	}

	expectedTraceIDs := make([]model.TraceID, testNumTraces)
	traceIDValues := make([]driver.Value, testNumTraces)
	for i := range expectedTraceIDs {
		traceID := model.TraceID{Low: uint64(i)}
		expectedTraceIDs[i] = traceID
		traceIDValues[i] = traceID.String()
	}

	found := traceIDValues[:0]
	endArg := end
	for i := 0; i < maxProgressiveSteps; i++ {
		if i == maxProgressiveSteps-1 {
			duration = fullDuration
		}

		startArg := endArg.Add(-duration)
		if startArg.Before(start) {
			startArg = start
		}

		// Select how many spans query will return
		index := int(math.Min(float64(i*2+1), testNumTraces))
		if i == maxProgressiveSteps-1 {
			index = testNumTraces
		}
		args := append(
			append(
				[]driver.Value{
					service,
					startArg,
					endArg,
				},
				found...),
			testNumTraces-len(found))
		mock.
			ExpectQuery(fmt.Sprintf(
				"SELECT DISTINCT traceID FROM %s WHERE service = ? AND timestamp >= ? AND timestamp <= ?%s ORDER BY service, timestamp DESC LIMIT ?",
				testIndexTable,
				func() string {
					if len(found) == 0 {
						return ""
					}
					return " AND traceID NOT IN (?" + strings.Repeat(",?", len(found)-1) + ")"
				}(),
			)).
			WithArgs(args...).
			WillReturnRows(getRows(traceIDValues[len(found):index]))
		endArg = startArg
		duration *= 2
		found = traceIDValues[:index]
	}

	traceIDs, err := traceReader.FindTraceIDs(context.Background(), &params)
	require.NoError(t, err)
	assert.Equal(t, expectedTraceIDs, traceIDs)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTraceReader_FindTraceIDsShortDurationAfterReduction(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)
	service := "service"
	start := testStartTime
	end := start.Add(8 * time.Hour)
	fullDuration := end.Sub(start)
	duration := minTimespanForProgressiveSearch
	params := spanstore.TraceQueryParameters{
		ServiceName:  service,
		NumTraces:    testNumTraces,
		StartTimeMin: start,
		StartTimeMax: end,
	}

	expectedTraceIDs := make([]model.TraceID, testNumTraces)
	traceIDValues := make([]driver.Value, testNumTraces)
	for i := range expectedTraceIDs {
		traceID := model.TraceID{Low: uint64(i)}
		expectedTraceIDs[i] = traceID
		traceIDValues[i] = traceID.String()
	}

	found := traceIDValues[:0]
	endArg := end
	for i := 0; i < maxProgressiveSteps; i++ {
		if i == maxProgressiveSteps-1 {
			duration = fullDuration
		}

		startArg := endArg.Add(-duration)
		if startArg.Before(start) {
			startArg = start
		}

		index := func() int {
			switch i {
			case 0:
				return 1
			case 1:
				return 3
			case 2:
				return 5
			default:
				return testNumTraces
			}
		}()
		args := append(
			append(
				[]driver.Value{
					service,
					startArg,
					endArg,
				},
				found...),
			testNumTraces-len(found))
		mock.
			ExpectQuery(fmt.Sprintf(
				"SELECT DISTINCT traceID FROM %s WHERE service = ? AND timestamp >= ? AND timestamp <= ?%s ORDER BY service, timestamp DESC LIMIT ?",
				testIndexTable,
				func() string {
					if len(found) == 0 {
						return ""
					}
					return " AND traceID NOT IN (?" + strings.Repeat(",?", len(found)-1) + ")"
				}(),
			)).
			WithArgs(args...).
			WillReturnRows(getRows(traceIDValues[len(found):index]))
		endArg = startArg
		duration *= 2
		found = traceIDValues[:index]
	}

	traceIDs, err := traceReader.FindTraceIDs(context.Background(), &params)
	require.NoError(t, err)
	assert.Equal(t, expectedTraceIDs, traceIDs)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTraceReader_FindTraceIDsEarlyExit(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)
	service := "service"
	start := testStartTime
	end := start.Add(24 * time.Hour)
	duration := end.Sub(start)
	for i := 0; i < maxProgressiveSteps; i++ {
		duration /= 2
	}
	params := spanstore.TraceQueryParameters{
		ServiceName:  service,
		NumTraces:    testNumTraces,
		StartTimeMin: start,
		StartTimeMax: end,
	}

	expectedTraceIDs := make([]model.TraceID, testNumTraces)
	traceIDValues := make([]driver.Value, testNumTraces)
	for i := range expectedTraceIDs {
		traceID := model.TraceID{Low: uint64(i)}
		expectedTraceIDs[i] = traceID
		traceIDValues[i] = traceID.String()
	}

	endArg := end
	startArg := endArg.Add(-duration)
	if startArg.Before(start) {
		startArg = start
	}

	mock.
		ExpectQuery(fmt.Sprintf(
			"SELECT DISTINCT traceID FROM %s WHERE service = ? AND timestamp >= ? AND timestamp <= ? ORDER BY service, timestamp DESC LIMIT ?",
			testIndexTable,
		)).
		WithArgs(
			service,
			startArg,
			endArg,
			testNumTraces,
		).
		WillReturnRows(getRows(traceIDValues))

	traceIDs, err := traceReader.FindTraceIDs(context.Background(), &params)
	require.NoError(t, err)
	assert.Equal(t, expectedTraceIDs, traceIDs)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTraceReader_FindTraceIDsShortRange(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)
	service := "service"
	start := testStartTime
	end := start.Add(time.Hour)
	params := spanstore.TraceQueryParameters{
		ServiceName:  service,
		NumTraces:    testNumTraces,
		StartTimeMin: start,
		StartTimeMax: end,
	}

	expectedTraceIDs := make([]model.TraceID, testNumTraces)
	traceIDValues := make([]driver.Value, testNumTraces)
	for i := range expectedTraceIDs {
		traceID := model.TraceID{Low: uint64(i)}
		expectedTraceIDs[i] = traceID
		traceIDValues[i] = traceID.String()
	}

	mock.
		ExpectQuery(fmt.Sprintf(
			"SELECT DISTINCT traceID FROM %s WHERE service = ? AND timestamp >= ? AND timestamp <= ? ORDER BY service, timestamp DESC LIMIT ?",
			testIndexTable,
		)).
		WithArgs(
			service,
			start,
			end,
			testNumTraces,
		).
		WillReturnRows(getRows(traceIDValues))

	traceIDs, err := traceReader.FindTraceIDs(context.Background(), &params)
	require.NoError(t, err)
	assert.Equal(t, expectedTraceIDs, traceIDs)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTraceReader_FindTraceIDsQueryError(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)
	service := "service"
	start := testStartTime
	end := start.Add(24 * time.Hour)
	duration := end.Sub(start)
	for i := 0; i < maxProgressiveSteps; i++ {
		duration /= 2
	}
	params := spanstore.TraceQueryParameters{
		ServiceName:  service,
		NumTraces:    testNumTraces,
		StartTimeMin: start,
		StartTimeMax: end,
	}

	mock.
		ExpectQuery(fmt.Sprintf(
			"SELECT DISTINCT traceID FROM %s WHERE service = ? AND timestamp >= ? AND timestamp <= ? ORDER BY service, timestamp DESC LIMIT ?",
			testIndexTable,
		)).
		WithArgs(
			service,
			end.Add(-duration),
			end,
			testNumTraces,
		).
		WillReturnError(errorMock)

	traceIDs, err := traceReader.FindTraceIDs(context.Background(), &params)
	require.ErrorIs(t, err, errorMock)
	assert.Equal(t, []model.TraceID(nil), traceIDs)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTraceReader_FindTraceIDsZeroStartTime(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)
	service := "service"
	start := time.Time{}
	end := testStartTime
	params := spanstore.TraceQueryParameters{
		ServiceName:  service,
		NumTraces:    testNumTraces,
		StartTimeMin: start,
		StartTimeMax: end,
	}

	traceIDs, err := traceReader.FindTraceIDs(context.Background(), &params)
	require.ErrorIs(t, err, errStartTimeRequired)
	assert.Equal(t, []model.TraceID(nil), traceIDs)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTraceReader_GetServices(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)
	expectedServices := []string{"GET /first", "POST /second", "PUT /third"}
	expectedServiceValues := make([]driver.Value, len(expectedServices))
	for i := range expectedServices {
		expectedServiceValues[i] = expectedServices[i]
	}
	queryResult := getRows(expectedServiceValues)

	mock.
		ExpectQuery(fmt.Sprintf("SELECT service FROM %s GROUP BY service", testOperationsTable)).
		WillReturnRows(queryResult)

	services, err := traceReader.GetServices(context.Background())
	require.NoError(t, err)
	assert.Equal(t, expectedServices, services)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTraceReader_GetServicesQueryError(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)

	mock.
		ExpectQuery(fmt.Sprintf("SELECT service FROM %s GROUP BY service", testOperationsTable)).
		WillReturnError(errorMock)
	services, err := traceReader.GetServices(context.Background())
	require.ErrorIs(t, err, errorMock)
	assert.Equal(t, []string(nil), services)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTraceReader_GetServicesNoTable(t *testing.T) {
	db, _, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, "", testIndexTable, testSpansTable)

	services, err := traceReader.GetServices(context.Background())
	require.ErrorIs(t, err, errNoOperationsTable)
	assert.Equal(t, []string(nil), services)
}

func TestTraceReader_GetOperations(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)
	service := "test service"
	params := spanstore.OperationQueryParameters{ServiceName: service}
	tests := map[string]struct {
		rows     *sqlmock.Rows
		expected []spanstore.Operation
	}{
		"default": {
			rows: sqlmock.NewRows([]string{"operation", "spankind"}).
				AddRow("operation_1", "client").
				AddRow("operation_2", ""),
			expected: []spanstore.Operation{{Name: "operation_1", SpanKind: "client"}, {Name: "operation_2"}},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			mock.
				ExpectQuery(fmt.Sprintf("SELECT operation, spankind FROM %s WHERE service = ? GROUP BY operation, spankind ORDER BY operation", testOperationsTable)).
				WithArgs(service).
				WillReturnRows(test.rows)

			operations, err := traceReader.GetOperations(context.Background(), params)
			require.NoError(t, err)
			assert.Equal(t, test.expected, operations)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestTraceReader_GetOperationsQueryError(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)
	service := "test service"
	params := spanstore.OperationQueryParameters{ServiceName: service}
	mock.
		ExpectQuery(fmt.Sprintf("SELECT operation, spankind FROM %s WHERE service = ? GROUP BY operation, spankind ORDER BY operation", testOperationsTable)).
		WithArgs(service).
		WillReturnError(errorMock)

	operations, err := traceReader.GetOperations(context.Background(), params)
	assert.ErrorIs(t, err, errorMock)
	assert.Equal(t, []spanstore.Operation(nil), operations)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTraceReader_GetOperationsNoTable(t *testing.T) {
	db, _, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, "", testIndexTable, testSpansTable)
	service := "test service"
	params := spanstore.OperationQueryParameters{ServiceName: service}
	operations, err := traceReader.GetOperations(context.Background(), params)
	assert.ErrorIs(t, err, errNoOperationsTable)
	assert.Equal(t, []spanstore.Operation(nil), operations)
}

func TestTraceReader_GetTrace(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)
	traceID := model.TraceID{High: 0, Low: 1}
	spanRefs := generateRandomSpans(testSpansInTrace)
	trace := model.Trace{}
	for _, span := range spanRefs {
		span.TraceID = traceID
		trace.Spans = append(trace.Spans, span)
	}
	spans := make([]model.Span, len(spanRefs))
	for i := range spanRefs {
		spans[i] = *spanRefs[i]
	}

	tests := map[string]struct {
		queryResult   *sqlmock.Rows
		expectedTrace *model.Trace
		expectedError error
	}{
		"json": {
			queryResult:   getEncodedSpans(spans, func(span *model.Span) ([]byte, error) { return json.Marshal(span) }),
			expectedTrace: &trace,
			expectedError: nil,
		},
		"protobuf": {
			queryResult:   getEncodedSpans(spans, func(span *model.Span) ([]byte, error) { return proto.Marshal(span) }),
			expectedTrace: &trace,
			expectedError: nil,
		},
		"trace not found": {
			queryResult:   sqlmock.NewRows([]string{"model"}),
			expectedTrace: nil,
			expectedError: spanstore.ErrTraceNotFound,
		},
		"query error": {
			queryResult:   getEncodedSpans(spans, func(span *model.Span) ([]byte, error) { return json.Marshal(span) }).RowError(0, errorMock),
			expectedTrace: nil,
			expectedError: errorMock,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			mock.
				ExpectQuery(
					fmt.Sprintf("SELECT model FROM %s PREWHERE traceID IN (?)", testSpansTable),
				).
				WithArgs(traceID).
				WillReturnRows(test.queryResult)

			trace, err := traceReader.GetTrace(context.Background(), traceID)
			require.ErrorIs(t, err, test.expectedError)
			if trace != nil {
				model.SortTrace(trace)
			}
			if test.expectedTrace != nil {
				model.SortTrace(test.expectedTrace)
			}
			assert.Equal(t, test.expectedTrace, trace)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSpanWriter_getTraces(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)
	traceIDs := []model.TraceID{
		{High: 0, Low: 1},
		{High: 2, Low: 2},
		{High: 1, Low: 3},
		{High: 0, Low: 4},
	}
	spans := make([]model.Span, testSpansInTrace*len(traceIDs))
	for i := 0; i < testSpansInTrace*len(traceIDs); i++ {
		traceID := traceIDs[i%len(traceIDs)]
		spans[i] = generateRandomSpan()
		spans[i].TraceID = traceID
	}

	traceIDStrings := make([]driver.Value, 4)
	for i, traceID := range traceIDs {
		traceIDStrings[i] = traceID.String()
	}

	tests := map[string]struct {
		queryResult    *sqlmock.Rows
		expectedTraces []*model.Trace
	}{
		"JSON encoded traces one span per trace": {
			queryResult:    getEncodedSpans(spans[:len(traceIDs)], func(span *model.Span) ([]byte, error) { return json.Marshal(span) }),
			expectedTraces: getTracesFromSpans(spans[:len(traceIDs)]),
		},
		"Protobuf encoded traces one span per trace": {
			queryResult:    getEncodedSpans(spans[:len(traceIDs)], func(span *model.Span) ([]byte, error) { return proto.Marshal(span) }),
			expectedTraces: getTracesFromSpans(spans[:len(traceIDs)]),
		},
		"JSON encoded traces many spans per trace": {
			queryResult:    getEncodedSpans(spans, func(span *model.Span) ([]byte, error) { return json.Marshal(span) }),
			expectedTraces: getTracesFromSpans(spans),
		},
		"Protobuf encoded traces many spans per trace": {
			queryResult:    getEncodedSpans(spans, func(span *model.Span) ([]byte, error) { return proto.Marshal(span) }),
			expectedTraces: getTracesFromSpans(spans),
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			mock.
				ExpectQuery(
					fmt.Sprintf("SELECT model FROM %s PREWHERE traceID IN (?,?,?,?)", testSpansTable),
				).
				WithArgs(traceIDStrings...).
				WillReturnRows(test.queryResult)

			traces, err := traceReader.getTraces(context.Background(), traceIDs)
			require.NoError(t, err)
			model.SortTraces(traces)
			assert.Equal(t, test.expectedTraces, traces)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSpanWriter_getTracesIncorrectData(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)
	traceIDs := []model.TraceID{
		{High: 0, Low: 1},
		{High: 2, Low: 2},
		{High: 1, Low: 3},
		{High: 0, Low: 4},
	}
	spans := make([]model.Span, 2*len(traceIDs))
	for i := 0; i < 2*len(traceIDs); i++ {
		traceID := traceIDs[i%len(traceIDs)]
		spans[i] = generateRandomSpan()
		spans[i].TraceID = traceID
	}

	traceIDStrings := make([]driver.Value, 4)
	for i, traceID := range traceIDs {
		traceIDStrings[i] = traceID.String()
	}

	tests := map[string]struct {
		queryResult    *sqlmock.Rows
		expectedResult []*model.Trace
		expectedError  error
	}{
		"JSON encoding incorrect data": {
			queryResult:    getRows([]driver.Value{[]byte{'{', 'n', 'o', 't', '_', 'a', '_', 'k', 'e', 'y', '}'}}),
			expectedResult: []*model.Trace(nil),
			expectedError:  fmt.Errorf("invalid character 'n' looking for beginning of object key string"),
		},
		"Protobuf encoding incorrect data": {
			queryResult:    getRows([]driver.Value{[]byte{'i', 'n', 'c', 'o', 'r', 'r', 'e', 'c', 't'}}),
			expectedResult: []*model.Trace{},
			expectedError:  nil,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			mock.
				ExpectQuery(
					fmt.Sprintf("SELECT model FROM %s PREWHERE traceID IN (?,?,?,?)", testSpansTable),
				).
				WithArgs(traceIDStrings...).
				WillReturnRows(test.queryResult)

			traces, err := traceReader.getTraces(context.Background(), traceIDs)
			if test.expectedError == nil {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, test.expectedError.Error())
			}
			assert.Equal(t, test.expectedResult, traces)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSpanWriter_getTracesQueryError(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)
	traceIDs := []model.TraceID{
		{High: 0, Low: 1},
		{High: 2, Low: 2},
		{High: 1, Low: 3},
		{High: 0, Low: 4},
	}

	traceIDStrings := make([]driver.Value, 4)
	for i, traceID := range traceIDs {
		traceIDStrings[i] = traceID.String()
	}

	mock.
		ExpectQuery(
			fmt.Sprintf("SELECT model FROM %s PREWHERE traceID IN (?,?,?,?)", testSpansTable),
		).
		WithArgs(traceIDStrings...).
		WillReturnError(errorMock)

	traces, err := traceReader.getTraces(context.Background(), traceIDs)
	assert.EqualError(t, err, errorMock.Error())
	assert.Equal(t, []*model.Trace(nil), traces)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestSpanWriter_getTracesRowsScanError(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)
	traceIDs := []model.TraceID{
		{High: 0, Low: 1},
		{High: 2, Low: 2},
		{High: 1, Low: 3},
		{High: 0, Low: 4},
	}

	traceIDStrings := make([]driver.Value, 4)
	for i, traceID := range traceIDs {
		traceIDStrings[i] = traceID.String()
	}
	rows := getRows([]driver.Value{"some value"}).RowError(0, errorMock)

	mock.
		ExpectQuery(
			fmt.Sprintf("SELECT model FROM %s PREWHERE traceID IN (?,?,?,?)", testSpansTable),
		).
		WithArgs(traceIDStrings...).
		WillReturnRows(rows)

	traces, err := traceReader.getTraces(context.Background(), traceIDs)
	assert.EqualError(t, err, errorMock.Error())
	assert.Equal(t, []*model.Trace(nil), traces)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestSpanWriter_getTraceNoTraceIDs(t *testing.T) {
	db, _, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)
	traceIDs := make([]model.TraceID, 0)

	traces, err := traceReader.getTraces(context.Background(), traceIDs)
	require.NoError(t, err)
	assert.Equal(t, make([]*model.Trace, 0), traces)
}

func getEncodedSpans(spans []model.Span, marshal func(span *model.Span) ([]byte, error)) *sqlmock.Rows {
	serialized := make([]driver.Value, len(spans))
	for i := range spans {
		bytes, err := marshal(&spans[i])
		if err != nil {
			panic(err)
		}
		serialized[i] = bytes
	}
	return getRows(serialized)
}

func getRows(values []driver.Value) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"model"})
	for _, value := range values {
		rows.AddRow(value)
	}
	return rows
}

func getTracesFromSpans(spans []model.Span) []*model.Trace {
	traces := make(map[model.TraceID]*model.Trace)
	for i, span := range spans {
		if _, ok := traces[span.TraceID]; !ok {
			traces[span.TraceID] = &model.Trace{}
		}
		traces[span.TraceID].Spans = append(traces[span.TraceID].Spans, &spans[i])
	}

	res := make([]*model.Trace, 0, len(traces))
	for _, trace := range traces {
		res = append(res, trace)
	}
	model.SortTraces(res)
	return res
}

func TestSpanWriter_findTraceIDsInRange(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)
	service := "test_service"
	operation := "test_operation"
	start := time.Unix(0, 0)
	end := time.Now()
	minDuration := time.Minute
	maxDuration := time.Hour
	tags := map[string]string{
		"key": "value",
	}
	skip := []model.TraceID{
		{High: 1, Low: 1},
		{High: 0, Low: 0},
	}
	tagArgs := func(tags map[string]string) []model.KeyValue {
		res := make([]model.KeyValue, 0, len(tags))
		for key, value := range tags {
			res = append(res, model.String(key, value))
		}
		return res
	}(tags)
	rowValues := []driver.Value{
		"1",
		"2",
		"3",
	}
	rows := []model.TraceID{
		{High: 0, Low: 1},
		{High: 0, Low: 2},
		{High: 0, Low: 3},
	}

	tests := map[string]struct {
		queryParams   spanstore.TraceQueryParameters
		skip          []model.TraceID
		expectedQuery string
		expectedArgs  []driver.Value
	}{
		"default": {
			queryParams: spanstore.TraceQueryParameters{ServiceName: service, NumTraces: testNumTraces},
			skip:        make([]model.TraceID, 0),
			expectedQuery: fmt.Sprintf(
				"SELECT DISTINCT traceID FROM %s WHERE service = ? AND timestamp >= ? AND timestamp <= ? ORDER BY service, timestamp DESC LIMIT ?",
				testIndexTable,
			),
			expectedArgs: []driver.Value{
				service,
				start,
				end,
				testNumTraces,
			},
		},
		"maxDuration": {
			queryParams: spanstore.TraceQueryParameters{ServiceName: service, NumTraces: testNumTraces, DurationMax: maxDuration},
			skip:        make([]model.TraceID, 0),
			expectedQuery: fmt.Sprintf(
				"SELECT DISTINCT traceID FROM %s WHERE service = ? AND timestamp >= ? AND timestamp <= ? AND durationUs <= ? ORDER BY service, timestamp DESC LIMIT ?",
				testIndexTable,
			),
			expectedArgs: []driver.Value{
				service,
				start,
				end,
				maxDuration.Microseconds(),
				testNumTraces,
			},
		},
		"minDuration": {
			queryParams: spanstore.TraceQueryParameters{ServiceName: service, NumTraces: testNumTraces, DurationMin: minDuration},
			skip:        make([]model.TraceID, 0),
			expectedQuery: fmt.Sprintf(
				"SELECT DISTINCT traceID FROM %s WHERE service = ? AND timestamp >= ? AND timestamp <= ? AND durationUs >= ? ORDER BY service, timestamp DESC LIMIT ?",
				testIndexTable,
			),
			expectedArgs: []driver.Value{
				service,
				start,
				end,
				minDuration.Microseconds(),
				testNumTraces,
			},
		},
		"tags": {
			queryParams: spanstore.TraceQueryParameters{ServiceName: service, NumTraces: testNumTraces, Tags: tags},
			skip:        make([]model.TraceID, 0),
			expectedQuery: fmt.Sprintf(
				"SELECT DISTINCT traceID FROM %s WHERE service = ? AND timestamp >= ? AND timestamp <= ?%s ORDER BY service, timestamp DESC LIMIT ?",
				testIndexTable,
				strings.Repeat(" AND has(tags.key, ?) AND tags.value[indexOf(tags.key, ?)] == ?", len(tags)),
			),
			expectedArgs: []driver.Value{
				service,
				start,
				end,
				tagArgs[0].Key,
				tagArgs[0].Key,
				tagArgs[0].AsString(),
				testNumTraces,
			},
		},
		"skip": {
			queryParams: spanstore.TraceQueryParameters{ServiceName: service, NumTraces: testNumTraces},
			skip:        skip,
			expectedQuery: fmt.Sprintf(
				"SELECT DISTINCT traceID FROM %s WHERE service = ? AND timestamp >= ? AND timestamp <= ? AND traceID NOT IN (?,?) ORDER BY service, timestamp DESC LIMIT ?",
				testIndexTable,
			),
			expectedArgs: []driver.Value{
				service,
				start,
				end,
				skip[0].String(),
				skip[1].String(),
				testNumTraces - len(skip),
			},
		},
		"operation": {
			queryParams: spanstore.TraceQueryParameters{ServiceName: service, NumTraces: testNumTraces, OperationName: operation},
			skip:        make([]model.TraceID, 0),
			expectedQuery: fmt.Sprintf(
				"SELECT DISTINCT traceID FROM %s WHERE service = ? AND operation = ? AND timestamp >= ? AND timestamp <= ? ORDER BY service, timestamp DESC LIMIT ?",
				testIndexTable,
			),
			expectedArgs: []driver.Value{
				service,
				operation,
				start,
				end,
				testNumTraces,
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			queryResult := sqlmock.NewRows([]string{"traceID"})
			for _, row := range rowValues {
				queryResult.AddRow(row)
			}

			mock.
				ExpectQuery(test.expectedQuery).
				WithArgs(test.expectedArgs...).
				WillReturnRows(queryResult)

			res, err := traceReader.findTraceIDsInRange(
				context.Background(),
				&test.queryParams,
				start,
				end,
				test.skip)
			require.NoError(t, err)
			assert.Equal(t, rows, res)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSpanReader_findTraceIDsInRangeNoIndexTable(t *testing.T) {
	db, _, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, "", testSpansTable)
	res, err := traceReader.findTraceIDsInRange(
		context.Background(),
		nil,
		time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2000, 1, 2, 0, 0, 0, 0, time.UTC),
		make([]model.TraceID, 0),
	)
	assert.Equal(t, []model.TraceID(nil), res)
	assert.EqualError(t, err, errNoIndexTable.Error())
}

func TestSpanReader_findTraceIDsInRangeEndBeforeStart(t *testing.T) {
	db, _, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)
	res, err := traceReader.findTraceIDsInRange(
		context.Background(),
		nil,
		time.Date(2000, 1, 2, 0, 0, 0, 0, time.UTC),
		time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		make([]model.TraceID, 0),
	)
	assert.Equal(t, make([]model.TraceID, 0), res)
	assert.NoError(t, err)
}

func TestSpanReader_findTraceIDsInRangeQueryError(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)
	service := "test_service"
	start := time.Unix(0, 0)
	end := time.Now()

	mock.
		ExpectQuery(fmt.Sprintf(
			"SELECT DISTINCT traceID FROM %s WHERE service = ? AND timestamp >= ? AND timestamp <= ? ORDER BY service, timestamp DESC LIMIT ?",
			testIndexTable,
		)).
		WithArgs(
			service,
			start,
			end,
			testNumTraces,
		).
		WillReturnError(errorMock)

	res, err := traceReader.findTraceIDsInRange(
		context.Background(),
		&spanstore.TraceQueryParameters{ServiceName: service, NumTraces: testNumTraces},
		start,
		end,
		make([]model.TraceID, 0))
	assert.EqualError(t, err, errorMock.Error())
	assert.Equal(t, []model.TraceID(nil), res)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestSpanReader_findTraceIDsInRangeIncorrectData(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)
	service := "test_service"
	start := time.Unix(0, 0)
	end := time.Now()
	rowValues := []driver.Value{
		"1",
		"incorrect value",
		"3",
	}
	queryResult := sqlmock.NewRows([]string{"traceID"})
	for _, row := range rowValues {
		queryResult.AddRow(row)
	}

	mock.
		ExpectQuery(fmt.Sprintf(
			"SELECT DISTINCT traceID FROM %s WHERE service = ? AND timestamp >= ? AND timestamp <= ? ORDER BY service, timestamp DESC LIMIT ?",
			testIndexTable,
		)).
		WithArgs(
			service,
			start,
			end,
			testNumTraces,
		).
		WillReturnRows(queryResult)

	res, err := traceReader.findTraceIDsInRange(
		context.Background(),
		&spanstore.TraceQueryParameters{ServiceName: service, NumTraces: testNumTraces},
		start,
		end,
		make([]model.TraceID, 0))
	assert.Error(t, err)
	assert.Equal(t, []model.TraceID(nil), res)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestSpanReader_getStrings(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	query := "SELECT b FROM a WHERE b != ?"
	argValues := []driver.Value{driver.Value("a")}
	args := []interface{}{"a"}
	rows := []driver.Value{"some", "query", "rows"}
	expectedResult := []string{"some", "query", "rows"}
	result := sqlmock.NewRows([]string{"b"})
	for _, str := range rows {
		result.AddRow(str)
	}
	mock.ExpectQuery(query).WithArgs(argValues...).WillReturnRows(result)

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)

	queryResult, err := traceReader.getStrings(context.Background(), query, args...)
	assert.NoError(t, err)
	assert.EqualValues(t, expectedResult, queryResult)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestSpanReader_getStringsQueryError(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	query := "SELECT b FROM a WHERE b != ?"
	argValues := []driver.Value{driver.Value("a")}
	args := []interface{}{"a"}
	mock.ExpectQuery(query).WithArgs(argValues...).WillReturnError(errorMock)

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)

	queryResult, err := traceReader.getStrings(context.Background(), query, args...)
	assert.EqualError(t, err, errorMock.Error())
	assert.EqualValues(t, []string(nil), queryResult)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestSpanReader_getStringsRowError(t *testing.T) {
	db, mock, err := mocks.GetDbMock()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")
	defer db.Close()

	query := "SELECT b FROM a WHERE b != ?"
	argValues := []driver.Value{driver.Value("a")}
	args := []interface{}{"a"}
	rows := []driver.Value{"some", "query", "rows"}
	result := sqlmock.NewRows([]string{"b"})
	for _, str := range rows {
		result.AddRow(str)
	}
	result.RowError(2, errorMock)
	mock.ExpectQuery(query).WithArgs(argValues...).WillReturnRows(result)

	traceReader := NewTraceReader(db, testOperationsTable, testIndexTable, testSpansTable)

	queryResult, err := traceReader.getStrings(context.Background(), query, args...)
	assert.EqualError(t, err, errorMock.Error())
	assert.EqualValues(t, []string(nil), queryResult)
	assert.NoError(t, mock.ExpectationsWereMet())
}
