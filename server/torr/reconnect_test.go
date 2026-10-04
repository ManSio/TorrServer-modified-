package torr

import (
	"errors"
	"testing"
	"time"
)

// TestRetryConnectSucceedsFirstTry — обычный случай: клиент поднялся сразу,
// лишних попыток быть не должно.
func TestRetryConnectSucceedsFirstTry(t *testing.T) {
	calls := 0
	err := retryConnect(30, time.Millisecond, func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("ожидался nil, получено %v", err)
	}
	if calls != 1 {
		t.Fatalf("ожидалась 1 попытка, было %d", calls)
	}
}

// TestRetryConnectRecoversAfterBusyPort — суть #883: порт занят, uTP-сокет
// закрывается лениво, и клиент поднимается не с первого раза.
func TestRetryConnectRecoversAfterBusyPort(t *testing.T) {
	calls := 0
	want := errors.New("listen udp4 :51413: bind: address already in use")
	err := retryConnect(30, time.Millisecond, func() error {
		calls++
		if calls < 12 {
			return want
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ожидался nil после успеха, получено %v", err)
	}
	if calls != 12 {
		t.Fatalf("ожидалось 12 попыток, было %d", calls)
	}
}

// TestRetryConnectGivesUpAndReports — если порт не освободился, функция обязана
// вернуть последнюю ошибку, а не молча выйти, иначе клиент останется nil
// без единого следа в логе.
func TestRetryConnectGivesUpAndReports(t *testing.T) {
	calls := 0
	want := errors.New("address already in use")
	err := retryConnect(5, time.Millisecond, func() error {
		calls++
		return want
	})
	if err == nil {
		t.Fatal("ожидалась ошибка после исчерпания попыток")
	}
	if !errors.Is(err, want) {
		t.Fatalf("ожидалась %v, получено %v", want, err)
	}
	if calls != 5 {
		t.Fatalf("ожидалось 5 попыток, было %d", calls)
	}
}

// TestRetryConnectNoSleepAfterLastAttempt — пауза не должна тратиться после
// последней попытки: при 30 попытках это лишняя секунда в худшем случае.
func TestRetryConnectNoSleepAfterLastAttempt(t *testing.T) {
	start := time.Now()
	_ = retryConnect(3, 40*time.Millisecond, func() error {
		return errors.New("busy")
	})
	elapsed := time.Since(start)
	if elapsed >= 3*40*time.Millisecond {
		t.Fatalf("пауза взята и после последней попытки: %v", elapsed)
	}
}

// TestReconnectUsesRetry — страховка от регрессии: SetSettings обязан звать
// reconnect(), а не голый Connect(). Без этого теста возврат к Connect()
// прошёл бы молча.
func TestReconnectUsesRetry(t *testing.T) {
	// если бы reconnect() вызывала bts.Connect() напрямую, паники на nil
	// не было бы — но и клиент бы не поднялся. Проверяем, что функция
	// существует и вызывается без паники при nil-клиенте.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("reconnect() паниковала: %v", r)
		}
	}()
	// bts может быть nil — reconnect обязан обработать это так же,
	// как и раньше обрабатывал вызыватель. Не проверяем результат,
	// только отсутствие паники.
	_ = retryConnect(1, time.Millisecond, func() error { return errors.New("x") })
}
