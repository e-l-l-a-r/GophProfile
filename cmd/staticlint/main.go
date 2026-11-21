// Package main предоставляет мультичекер staticlint, инструмент для статического анализа кода Go.
//
// В мультичекер включены только анализаторы, которые сообщают о проблемах в
// коде. Вспомогательные анализаторы (buildssa, ctrlflow, inspect и т. п.)
// подключаются автоматически как зависимости и отдельно не перечисляются.
// Проверки ассемблера и cgo не включены: в проекте нет ни того, ни другого.
//
// Основной набор:
//
// 1. Стандартные анализаторы из golang.org/x/tools/go/analysis/passes:
//   - appends: находит вызовы append с единственным аргументом.
//   - assign: проверяет наличие бесполезных присваиваний.
//   - atomic: проверяет наличие распространенных ошибок при использовании пакета sync/atomic.
//   - bools: проверяет наличие распространенных ошибок, связанных с логическими операторами.
//   - buildtag: проверяет, что теги сборки правильно сформированы и корректно расположены.
//   - composite: проверяет наличие неключевых составных литералов.
//   - copylock: проверяет наличие блокировок, ошибочно переданных по значению.
//   - deepequalerrors: проверяет использование reflect.DeepEqual с объектами ошибок.
//   - defers: находит defer с аргументами, вычисляемыми раньше, чем ожидается (time.Since).
//   - errorsas: проверяет, что второй аргумент errors.As является указателем на тип, реализующий error.
//   - httpresponse: проверяет наличие ошибок при использовании HTTP-ответов.
//   - ifaceassert: проверяет наличие невозможных утверждений типов интерфейс-интерфейс.
//   - loopclosure: проверяет наличие ссылок на переменные цикла из вложенных функций.
//   - lostcancel: проверяет отсутствие вызова функции отмены контекста.
//   - nilfunc: проверяет наличие бесполезных сравнений функций с nil.
//   - nilness: находит заведомо лишние или ошибочные сравнения с nil и разыменования nil.
//   - printf: проверяет согласованность строк формата Printf и аргументов.
//   - reflectvaluecompare: проверяет наличие сравнений между reflect.Value и значениями интерфейса.
//   - shift: проверяет наличие сдвигов, превышающих разрядность целого числа.
//   - sigchanyzer: проверяет наличие небуферизованных каналов сигналов.
//   - sortslice: проверяет вызовы sort.Slice, которые не используют тип среза.
//   - stdmethods: проверяет сигнатуры методов со стандартными именами.
//   - stringintconv: проверяет преобразования string(int).
//   - structtag: проверяет соответствие тегов полей структур reflect.StructTag.Get.
//   - testinggoroutine: проверяет вызовы Fatal из горутины, запущенной тестом.
//   - tests: проверяет наличие распространенных ошибок в функциях тестов и бенчмарков.
//   - timeformat: находит форматы времени с перепутанными местами месяцем и днём.
//   - unmarshal: проверяет передачу значений, не являющихся указателями или интерфейсами, в функции размаршаливания.
//   - unreachable: проверяет наличие недостижимого кода.
//   - unsafeptr: проверяет наличие неверных преобразований uintptr в unsafe.Pointer.
//   - unusedresult: проверяет отсутствие использования результатов вызовов определенных функций.
//   - unusedwrite: проверяет наличие неиспользуемых записей в поля структур или переменные.
//   - waitgroup: находит вызов WaitGroup.Add внутри запускаемой горутины.
//
// 2. Все анализаторы класса SA из staticcheck.io: корректность, безопасность и производительность.
//
// 3. Выбранные анализаторы из других классов staticcheck.io:
//   - S1000: Использование обычной отправки или приёма из канала вместо select с одним случаем.
//   - S1002: Пропуск сравнения с логической константой.
//   - S1005: Отказ от лишнего пустого идентификатора.
//   - S1008: Упрощение возврата логического выражения.
//   - S1009: Пропуск лишней проверки на nil для срезов, карт и каналов.
//   - S1011: Объединение двух срезов одним вызовом append.
//   - S1016: Использование преобразования типа вместо ручного копирования полей структуры.
//   - S1021: Объединение объявления переменной и присваивания.
//   - S1025: Отказ от лишнего fmt.Sprintf("%s", x).
//   - S1039: Отказ от лишнего вызова fmt.Sprint.
//   - ST1000: Неверная или отсутствующая документация пакета.
//   - ST1005: Неверное форматирование строк ошибок.
//   - QF1001: Применение закона Де Моргана.
//
// 4. Публичные анализаторы:
//   - nilerr (github.com/gostaticanalysis/nilerr): находит код, который возвращает nil, даже если ошибка не равна nil.
//   - errcheck (github.com/kisielk/errcheck): находит непроверенные ошибки в коде.
//
// 5. Пользовательский анализатор:
//   - osexitanalyzer: запрещает прямые вызовы os.Exit в функции main пакета main.
//
// Строгий набор включается переменной окружения STATICLINT_STRICT=1 и
// добавляет к основному шумные проверки, замечания которых требуют оценки:
//   - shadow: проверяет наличие затенённых переменных.
//   - fieldalignment: находит структуры, поля которых можно переупорядочить для экономии памяти.
package main

import (
	"os"
	"strings"

	"github.com/gostaticanalysis/nilerr"
	"github.com/kisielk/errcheck/errcheck"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/multichecker"
	"golang.org/x/tools/go/analysis/passes/appends"
	"golang.org/x/tools/go/analysis/passes/assign"
	"golang.org/x/tools/go/analysis/passes/atomic"
	"golang.org/x/tools/go/analysis/passes/bools"
	"golang.org/x/tools/go/analysis/passes/buildtag"
	"golang.org/x/tools/go/analysis/passes/composite"
	"golang.org/x/tools/go/analysis/passes/copylock"
	"golang.org/x/tools/go/analysis/passes/deepequalerrors"
	"golang.org/x/tools/go/analysis/passes/defers"
	"golang.org/x/tools/go/analysis/passes/errorsas"
	"golang.org/x/tools/go/analysis/passes/fieldalignment"
	"golang.org/x/tools/go/analysis/passes/httpresponse"
	"golang.org/x/tools/go/analysis/passes/ifaceassert"
	"golang.org/x/tools/go/analysis/passes/loopclosure"
	"golang.org/x/tools/go/analysis/passes/lostcancel"
	"golang.org/x/tools/go/analysis/passes/nilfunc"
	"golang.org/x/tools/go/analysis/passes/nilness"
	"golang.org/x/tools/go/analysis/passes/printf"
	"golang.org/x/tools/go/analysis/passes/reflectvaluecompare"
	"golang.org/x/tools/go/analysis/passes/shadow"
	"golang.org/x/tools/go/analysis/passes/shift"
	"golang.org/x/tools/go/analysis/passes/sigchanyzer"
	"golang.org/x/tools/go/analysis/passes/sortslice"
	"golang.org/x/tools/go/analysis/passes/stdmethods"
	"golang.org/x/tools/go/analysis/passes/stringintconv"
	"golang.org/x/tools/go/analysis/passes/structtag"
	"golang.org/x/tools/go/analysis/passes/testinggoroutine"
	"golang.org/x/tools/go/analysis/passes/tests"
	"golang.org/x/tools/go/analysis/passes/timeformat"
	"golang.org/x/tools/go/analysis/passes/unmarshal"
	"golang.org/x/tools/go/analysis/passes/unreachable"
	"golang.org/x/tools/go/analysis/passes/unsafeptr"
	"golang.org/x/tools/go/analysis/passes/unusedresult"
	"golang.org/x/tools/go/analysis/passes/unusedwrite"
	"golang.org/x/tools/go/analysis/passes/waitgroup"
	"honnef.co/go/tools/quickfix"
	"honnef.co/go/tools/simple"
	"honnef.co/go/tools/staticcheck"
	"honnef.co/go/tools/stylecheck"

	"github.com/e-l-l-a-r/gophprofile/cmd/staticlint/osexitanalyzer"
)

// strictEnv - переменная окружения, включающая строгий набор анализаторов.
const strictEnv = "STATICLINT_STRICT"

func main() {
	analyzers := baseAnalyzers()
	if os.Getenv(strictEnv) == "1" {
		analyzers = append(analyzers, strictAnalyzers()...)
	}

	multichecker.Main(analyzers...)
}

// strictAnalyzers возвращает шумные анализаторы строгого набора.
func strictAnalyzers() []*analysis.Analyzer {
	return []*analysis.Analyzer{
		shadow.Analyzer,
		fieldalignment.Analyzer,
	}
}

// baseAnalyzers возвращает анализаторы основного набора.
func baseAnalyzers() []*analysis.Analyzer {
	analyzers := []*analysis.Analyzer{
		// Стандартные анализаторы
		appends.Analyzer,
		assign.Analyzer,
		atomic.Analyzer,
		bools.Analyzer,
		buildtag.Analyzer,
		composite.Analyzer,
		copylock.Analyzer,
		deepequalerrors.Analyzer,
		defers.Analyzer,
		errorsas.Analyzer,
		httpresponse.Analyzer,
		ifaceassert.Analyzer,
		loopclosure.Analyzer,
		lostcancel.Analyzer,
		nilfunc.Analyzer,
		nilness.Analyzer,
		printf.Analyzer,
		reflectvaluecompare.Analyzer,
		shift.Analyzer,
		sigchanyzer.Analyzer,
		sortslice.Analyzer,
		stdmethods.Analyzer,
		stringintconv.Analyzer,
		structtag.Analyzer,
		testinggoroutine.Analyzer,
		tests.Analyzer,
		timeformat.Analyzer,
		unmarshal.Analyzer,
		unreachable.Analyzer,
		unsafeptr.Analyzer,
		unusedresult.Analyzer,
		unusedwrite.Analyzer,
		waitgroup.Analyzer,

		// Пользовательский анализатор
		osexitanalyzer.Analyzer,

		// Публичные анализаторы
		nilerr.Analyzer,
		errcheck.Analyzer,
	}

	// Анализаторы staticcheck SA
	for _, v := range staticcheck.Analyzers {
		if strings.HasPrefix(v.Analyzer.Name, "SA") {
			analyzers = append(analyzers, v.Analyzer)
		}
	}

	// Выбранные анализаторы класса S, упрощающие код.
	simpleChecks := map[string]bool{
		"S1000": true, // Использование обычной отправки или приёма из канала вместо select с одним случаем
		"S1002": true, // Пропуск сравнения с логической константой
		"S1005": true, // Отказ от лишнего пустого идентификатора
		"S1008": true, // Упрощение возврата логического выражения
		"S1009": true, // Пропуск лишней проверки на nil для срезов, карт и каналов
		"S1011": true, // Объединение двух срезов одним вызовом append
		"S1016": true, // Использование преобразования типа вместо ручного копирования полей структуры
		"S1021": true, // Объединение объявления переменной и присваивания
		"S1025": true, // Отказ от лишнего fmt.Sprintf("%s", x)
		"S1039": true, // Отказ от лишнего вызова fmt.Sprint
	}
	for _, v := range simple.Analyzers {
		if simpleChecks[v.Analyzer.Name] {
			analyzers = append(analyzers, v.Analyzer)
		}
	}
	for _, v := range stylecheck.Analyzers {
		// Неверная или отсутствующая документация пакета, форматирование строк ошибок
		if v.Analyzer.Name == "ST1000" || v.Analyzer.Name == "ST1005" {
			analyzers = append(analyzers, v.Analyzer)
		}
	}
	for _, v := range quickfix.Analyzers {
		if v.Analyzer.Name == "QF1001" { // Применение закона Де Моргана
			analyzers = append(analyzers, v.Analyzer)
			break
		}
	}

	return analyzers
}
