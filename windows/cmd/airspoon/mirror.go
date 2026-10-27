package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"airspoon/internal/airplay"
	"airspoon/internal/encode/ffmpeg"
	"airspoon/internal/encode/native"
)

// rememberReceiver сохраняет телевизор, на который пошёл показ, чтобы он
// появлялся в списке, даже если mDNS в сети не работает. path пуст — не сохранять
// (тесты). Ошибка записи не мешает показу.
func rememberReceiver(path, host string, port int, advert *airplay.AirPlayDevice, info *airplay.ReceiverInfo) {
	if path == "" {
		return
	}
	r := airplay.LastReceiver{Address: host, Port: port}
	if advert != nil {
		r.Name, r.DeviceID = advert.Name, advert.DeviceID
	}
	if info != nil {
		if info.Name != "" {
			r.Name = info.Name
		}
		if info.DeviceID != "" {
			r.DeviceID = info.DeviceID
		}
	}
	if err := airplay.SaveLastReceiver(path, r); err != nil {
		log.Printf("не удалось запомнить телевизор: %v", err)
	}
}

type mirrorOptions struct {
	// lastReceiverFile — куда запомнить телевизор после успешного старта показа; пусто — не запоминать.
	lastReceiverFile string
	name             string
	host             string
	timeout          time.Duration
	verbose          bool
	compat           string
	noAudio          bool
	pin              string
	forcePair        bool
	credFile         string
	duration         time.Duration
	stdin            io.Reader // источник ввода PIN; nil — os.Stdin

	// Поля ниже заполняет только режим трея (консольный режим их не трогает).
	// advert — приёмник, уже найденный по mDNS: поиск и разбор адреса пропускаются.
	advert *airplay.AirPlayDevice
	// pinPrompt заменяет ввод PIN в консоли: вызывается, когда приёмник просит
	// код (wrong — предыдущий код не подошёл). Ошибка (например, отмена) прерывает
	// показ и возвращается вызывающему. Неверный код при заданном pinPrompt не
	// останавливает показ: приёмник спрашивают заново.
	pinPrompt func(ctx context.Context, wrong bool) (string, error)
	// onStreaming вызывается, когда сопряжение и SETUP позади и пошли кадры.
	onStreaming func()
	// onNotice получает короткие тексты для пользователя по ходу подготовки показа
	// (например, «аппаратного кодера нет — качество снижено»); nil — не нужен.
	onNotice func(text string)

	width, height, fps, bitrate int
	display                     int
	noCursor                    bool
	ffmpegPath                  string
	encoder                     string // auto, hw, sw или ffmpeg; пусто — auto
	noGPUConvert                bool   // не преобразовывать цвет на GPU (видеопроцессор), только на CPU
}

func runMirror(args []string) int {
	var o mirrorOptions
	fs := flag.NewFlagSet("mirror", flag.ContinueOnError)
	fs.StringVar(&o.name, "name", "", "имя приёмника (например \"Телевизор\"), поиск по mDNS")
	fs.StringVar(&o.host, "host", "", "прямой адрес приёмника адрес[:порт] без mDNS (порт по умолчанию 7000)")
	fs.DurationVar(&o.timeout, "timeout", 3*time.Second, "сколько искать приёмник по имени")
	fs.BoolVar(&o.verbose, "verbose", false, "подробный лог каждого шага RTSP (без вывода ключей)")
	fs.StringVar(&o.compat, "compat", "auto", "режим совместимости: auto (по умолчанию: тип приёмника определяется по /info — UxPlay и AirSpoon Receiver ведутся как uxplay, Apple TV и телевизоры с AirPlay 2 — как generic), uxplay (как для UxPlay: raw-сопряжение, PIN через /pair-setup-pin, время только NTP, звук AES-CBC без RFC 2198, видео AES-CTR; канал HAP считается ошибкой) или generic (без ограничений UxPlay: HAP, транзиентное сопряжение, PTP по features приёмника; при отказе SETUP один раз повторяется попытка с обратным смешиванием ключа FairPlay)")
	fs.BoolVar(&o.noAudio, "no-audio", false, "не передавать звук")
	fs.StringVar(&o.pin, "pin", "", "PIN приёмника (лучше переменная AIRSPOON_PIN, чтобы PIN не попал в историю команд)")
	fs.BoolVar(&o.forcePair, "pair", false, "сопрячься заново, даже если ключи сохранены")
	fs.StringVar(&o.credFile, "creds", defaultCredentialsFile(), "файл с ключами сопряжения")
	fs.DurationVar(&o.duration, "duration", 0, "остановиться через указанное время (0 — до Ctrl+C)")
	fs.IntVar(&o.width, "width", 0, "ширина видео (0 — по холсту приёмника, но не больше 1920)")
	fs.IntVar(&o.height, "height", 0, "высота видео (0 — по холсту приёмника, но не больше 1080)")
	fs.IntVar(&o.fps, "fps", 0, "частота кадров (0 — по maxFPS приёмника, но не больше 60)")
	fs.IntVar(&o.bitrate, "bitrate", 0, "битрейт, кбит/с (0 — автоматически)")
	fs.IntVar(&o.display, "display", 0, "номер монитора для захвата")
	fs.BoolVar(&o.noCursor, "no-cursor", false, "не показывать курсор мыши")
	fs.StringVar(&o.encoder, "encoder", "auto", "кодер видео: auto (аппаратный Media Foundation, при отказе программный), hw (только аппаратный), sw (только программный Microsoft H.264) или ffmpeg (запасной путь для отладки, нужен ffmpeg.exe)")
	fs.BoolVar(&o.noGPUConvert, "no-gpu-convert", false, "не использовать видеопроцессор GPU: цвет и масштаб преобразует процессор (если на экране полосы или артефакты)")
	fs.StringVar(&o.ffmpegPath, "ffmpeg", os.Getenv("AIRSPOON_FFMPEG"), "путь к ffmpeg.exe (по умолчанию рядом с airspoon.exe или в PATH)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	setupLogging(o.verbose)
	o.lastReceiverFile = defaultLastReceiverFile()

	if (o.name == "") == (o.host == "") {
		fmt.Fprintln(os.Stderr, "укажите ровно одно из: --name или --host")
		return 2
	}
	if o.compat != "uxplay" && o.compat != "auto" && o.compat != "generic" {
		fmt.Fprintf(os.Stderr, "недопустимое значение --compat %q (нужно auto, uxplay или generic)\n", o.compat)
		return 2
	}
	if o.fps < 0 || o.fps > 60 {
		fmt.Fprintln(os.Stderr, "--fps должен быть от 1 до 60 (0 — по приёмнику)")
		return 2
	}
	if err := checkVideoSizeFlags(o.width, o.height); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if _, _, err := parseEncoder(o.encoder); err != nil {
		fmt.Fprintf(os.Stderr, "недопустимое значение --encoder: %v\n", err)
		return 2
	}
	if env := os.Getenv("AIRSPOON_PIN"); env != "" {
		o.pin = env
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if o.duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.duration)
		defer cancel()
	}

	if err := mirror(ctx, o); err != nil && ctx.Err() == nil {
		log.Printf("ошибка: %v", err)
		cliCaptureAdvice(err, func(s string) { fmt.Fprintln(os.Stderr, s) })
		return 1
	}
	log.Println("передача остановлена")
	return 0
}

// startVideoSource запускает источник видео на ffmpeg.exe; подменяется в тестах.
var startVideoSource = func(ctx context.Context, cfg ffmpeg.Config) (airplay.VideoSource, error) {
	s, err := ffmpeg.Start(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// startNativeSource запускает нативный источник (DXGI + Media Foundation);
// подменяется в тестах.
var startNativeSource = func(ctx context.Context, cfg native.Config) (airplay.VideoSource, error) {
	s, err := native.Start(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// parseEncoder разбирает значение --encoder: useFFmpeg — запасной источник на
// ffmpeg.exe, иначе нативный источник в режиме mode. Пустое значение — auto.
func parseEncoder(value string) (useFFmpeg bool, mode native.Mode, err error) {
	if value == "ffmpeg" {
		return true, native.ModeAuto, nil
	}
	if value == "" {
		value = "auto"
	}
	mode, err = native.ParseMode(value)
	return false, mode, err
}

func mirror(ctx context.Context, o mirrorOptions) error {
	warnIgnoredEncoderFlags(o, log.Printf)
	var in io.Reader = os.Stdin
	if o.stdin != nil {
		in = o.stdin
	}
	console := bufio.NewReader(in)

	// 1. Выбор приёмника.
	var host string
	var port int
	var advert *airplay.AirPlayDevice
	if o.advert != nil {
		advert = o.advert
		host, port = advert.IP, advert.Port
		log.Printf("приёмник %q выбран из списка: %s", advert.Name, net.JoinHostPort(host, strconv.Itoa(port)))
	} else if o.host != "" {
		var err error
		host, port, err = splitHostPort(o.host, 7000)
		if err != nil {
			return err
		}
		log.Printf("прямой адрес: %s (mDNS не используется)", net.JoinHostPort(host, strconv.Itoa(port)))
	} else {
		log.Printf("поиск приёмника %q (%s)...", o.name, o.timeout)
		devices, err := discover(ctx, o.timeout)
		if err != nil {
			return fmt.Errorf("поиск приёмников: %w", err)
		}
		if advert, err = findByName(devices, o.name); err != nil {
			return err
		}
		host, port = advert.IP, advert.Port
		log.Printf("найден: имя=%q адрес=%s модель=%q features=0x%x",
			advert.Name, net.JoinHostPort(host, strconv.Itoa(port)), advert.Model, advert.Features)
	}
	// mixOverride задаётся при повторе после отказа SETUP (см. ниже).
	var mixOverride *bool
	// useUxPlay — ограничивать ли клиента возможностями UxPlay. В режиме auto
	// решается после GET /info (см. ClassifyReceiver); до этого — как в явном uxplay.
	useUxPlay := o.compat != "generic"
	newClient := func() *airplay.AirPlayClient {
		var c *airplay.AirPlayClient
		if advert != nil {
			d := *advert
			d.IP, d.Port = host, port
			c = airplay.NewAirPlayClientForDevice(d)
		} else {
			c = airplay.NewAirPlayClient(host, port)
		}
		if useUxPlay {
			c.EnableUxPlayCompat()
		}
		if mixOverride != nil {
			c.SetFairPlayKeyMixing(*mixOverride)
		}
		return c
	}

	// 2. Подключение и GET /info.
	client := newClient()
	client.SetPassword(o.pin)
	log.Println("подключение к приёмнику...")
	if err := client.Connect(ctx); err != nil {
		return fmt.Errorf("подключение: %w", err)
	}
	defer func() { _ = client.Close() }()
	info, err := client.GetInfo()
	if err != nil {
		return fmt.Errorf("GET /info: %w", err)
	}
	logReceiverInfo(info)
	if o.compat == "auto" {
		class, reason := airplay.ClassifyReceiver(info)
		useUxPlay = class.UsesUxPlayProfile()
		if !useUxPlay {
			client.DisableUxPlayCompat()
		}
		profile := "generic (по features приёмника, HAP разрешён)"
		if useUxPlay {
			profile = "uxplay (raw-сопряжение, NTP, звук AES-CBC)"
		}
		log.Printf("тип приёмника: %s; профиль: %s; причина: %s", class, profile, reason)
	}

	// 3. Сопряжение.
	if err := os.MkdirAll(filepath.Dir(o.credFile), 0o700); err != nil {
		return fmt.Errorf("создание каталога ключей: %w", err)
	}
	store, err := airplay.NewCredentialStore(o.credFile)
	if err != nil {
		return fmt.Errorf("хранилище ключей %s: %w", o.credFile, err)
	}
	pin := o.pin
	reconnect := func() error {
		_ = client.Close()
		client = newClient()
		client.SetPassword(pin)
		if err := client.Connect(ctx); err != nil {
			return fmt.Errorf("повторное подключение: %w", err)
		}
		info, err = client.GetInfo()
		if err != nil {
			return fmt.Errorf("GET /info после повторного подключения: %w", err)
		}
		return nil
	}
	saveKeys := func() {
		if client.PairKeys == nil {
			return
		}
		if err := store.SavePairing(info.DeviceID, client.PairingID,
			client.PairKeys.Ed25519Public, client.PairKeys.Ed25519Private, client.PairingProtocol()); err != nil {
			log.Printf("предупреждение: ключи сопряжения не сохранены: %v", err)
		} else {
			log.Printf("ключи сопряжения сохранены в %s", o.credFile)
		}
	}
	pairWithPIN := func(expectPIN bool) error {
		wrong := false
		for {
			if pin == "" {
				if err := client.StartPINDisplay(); err != nil {
					log.Printf("предупреждение: приёмник не показал PIN: %v", err)
				}
				if o.pinPrompt != nil {
					p, perr := o.pinPrompt(ctx, wrong)
					if perr != nil {
						return fmt.Errorf("ввод PIN: %w", perr)
					}
					pin = strings.TrimSpace(p)
				} else {
					if expectPIN {
						fmt.Printf("\nНа экране приёмника %q должен появиться PIN.\nВведите PIN: ", info.Name)
					} else {
						fmt.Printf("\nПриёмник %q запросил код.\nВведите PIN или пароль: ", info.Name)
					}
					line, rerr := console.ReadString('\n')
					if rerr != nil && !errors.Is(rerr, io.EOF) {
						return fmt.Errorf("чтение PIN: %w", rerr)
					}
					pin = strings.TrimSpace(line)
				}
				if pin == "" {
					return errors.New("PIN не может быть пустым")
				}
			}
			err := client.Pair(ctx, pin)
			if err == nil {
				saveKeys()
				return nil
			}
			if errors.Is(err, airplay.ErrLegacyPINRejected) || errors.Is(err, airplay.ErrHAPPINRejected) {
				if o.pinPrompt != nil {
					// Показ из трея: приёмник сгенерирует новый код, спрашиваем заново
					// на чистом соединении.
					log.Printf("приёмник не принял код: %v; спрашиваем заново", err)
					pin, wrong = "", true
					if rerr := reconnect(); rerr != nil {
						return rerr
					}
					continue
				}
				return fmt.Errorf("сопряжение с PIN: приёмник не принял PIN, проверьте код на экране (%w)", err)
			}
			return fmt.Errorf("сопряжение с PIN: %w", err)
		}
	}
	freshPair := func() error {
		// UxPlay всегда отдаёт в /info statusFlags=0x44 и не сообщает о PIN
		// (признак есть только в mDNS). Если PIN задан пользователем, сначала
		// пробуем PIN-сопряжение UxPlay. Откат на обычное сопряжение допустим
		// только если приёмник не знает /pair-setup-pin; неверный PIN — сразу ошибка.
		if pin != "" && info.RequiredPairingCredential() == airplay.PairingCredentialNone &&
			!info.RequiresPassword() && info.PrefersLegacyPairing() {
			log.Println("PIN задан: пробуем PIN-сопряжение приёмника UxPlay (/pair-setup-pin)...")
			// Как и в интерактивном пути: до /pair-pin-start UxPlay со случайным PIN
			// не знает PIN (сверяет с «0000») и не показывает его на экране.
			if err := client.StartPINDisplay(); err != nil {
				log.Printf("предупреждение: приёмник не показал PIN: %v", err)
			}
			err := client.PairWithLegacyPIN(ctx, pin)
			switch {
			case err == nil:
				saveKeys()
				return nil
			case errors.Is(err, airplay.ErrLegacyPINUnsupported):
				// Приёмник не знает /pair-setup-pin (например, режим пароля):
				// единственный случай, когда обычное сопряжение уместно.
				log.Printf("приёмник не поддерживает PIN-сопряжение UxPlay (%v); продолжаем обычным сопряжением", err)
				if err := reconnect(); err != nil {
					return err
				}
			case errors.Is(err, airplay.ErrLegacyPINRejected):
				// Настоящий UxPlay пропускает raw pair-setup и без PIN, поэтому после
				// отказа продолжать нельзя: опечатка в PIN не должна давать подключение.
				return fmt.Errorf("сопряжение с PIN: приёмник не принял PIN, проверьте код на экране "+
					"(без --pin и AIRSPOON_PIN программа спросит его в консоли): %w", err)
			default:
				return fmt.Errorf("сопряжение с PIN: %w", err)
			}
		}
		switch info.RequiredPairingCredential() {
		case airplay.PairingCredentialPassword:
			return pairWithPIN(false)
		case airplay.PairingCredentialPIN:
			return pairWithPIN(true)
		}
		log.Println("сопряжение без PIN (pair-setup + pair-verify)...")
		if err := client.Pair(ctx, ""); err != nil {
			if info.RequiresPassword() {
				return fmt.Errorf("сопряжение с защищённым паролем приёмником: %w", err)
			}
			log.Printf("сопряжение без PIN не удалось (%v); пробуем с PIN на чистом соединении", err)
			if err := reconnect(); err != nil {
				return err
			}
			return pairWithPIN(false)
		}
		return nil
	}

	var saved *airplay.SavedCredentials
	if !o.forcePair {
		saved = store.Lookup(info.DeviceID)
	}
	if saved != nil && saved.HasPairingCredentials() {
		log.Println("найдены сохранённые ключи, выполняем pair-verify...")
		verr := client.RestorePairingCredentials(saved)
		if verr == nil {
			verr = client.PairVerify(ctx)
		}
		if verr != nil {
			log.Printf("pair-verify по сохранённым ключам не удался: %v; сопрягаемся заново", verr)
			if err := reconnect(); err != nil {
				return err
			}
			if err := freshPair(); err != nil {
				return err
			}
		}
	} else if err := freshPair(); err != nil {
		return err
	}
	log.Printf("сопряжение завершено (протокол: %s)", pairingProtocolName(client.PairingProtocol()))
	if useUxPlay && o.compat == "auto" && client.PairingProtocol() != airplay.PairingProtocolRaw {
		// Профиль UxPlay выбран по /info, но приёмник сопрягся по HAP: снимаем
		// ограничения (канал HAP с шифрованием UxPlay-профиль запрещает).
		log.Printf("приёмник сопрягся по %s, профиль UxPlay снят (режим auto)", pairingProtocolName(client.PairingProtocol()))
		useUxPlay = false
		client.DisableUxPlayCompat()
	}
	if useUxPlay && client.PairingProtocol() != airplay.PairingProtocolRaw {
		return fmt.Errorf("приёмник сопрягся не по legacy-протоколу UxPlay (raw pair-setup/pair-verify), а по %s; "+
			"режим --compat=uxplay не применим, попробуйте --compat=auto",
			pairingProtocolName(client.PairingProtocol()))
	}

	// 4. FairPlay.
	setupFairPlay := func() error {
		if client.FpEkey != nil {
			return nil
		}
		if err := client.FairPlaySetup(ctx); err != nil {
			if !errors.Is(err, airplay.ErrFairPlayUnsupported) {
				return fmt.Errorf("FairPlay: %w", err)
			}
			log.Printf("FairPlay SAP не поддерживается приёмником (%v); продолжаем без него", err)
		} else {
			log.Println("FairPlay setup выполнен")
		}
		return nil
	}
	if err := setupFairPlay(); err != nil {
		return err
	}

	// 5. SETUP зеркалирования; видеоисточник запускается из хука подготовки.
	streamCfg := airplay.StreamConfig{
		FPS:        o.fps,
		Bitrate:    o.bitrate,
		VideoCodec: airplay.VideoCodecH264,
		NoAudio:    o.noAudio,
	}
	var video airplay.VideoSource
	defer func() {
		if video != nil {
			_ = video.Close()
		}
	}()
	prepare := func(p airplay.VideoProfile) (airplay.VideoPreparationResult, error) {
		if video != nil {
			return airplay.VideoPreparationResult{}, nil
		}
		if p.Codec != airplay.VideoCodecH264 {
			return airplay.VideoPreparationResult{}, fmt.Errorf("приёмник выбрал кодек %s, поддерживается только H.264", p.Codec)
		}
		width, height := chooseVideoSize(p.Width, p.Height, o.width, o.height)
		bitrate := o.bitrate
		if bitrate <= 0 {
			bitrate = ffmpeg.AutoBitrateKbps(width, height, p.FPS)
		}
		log.Printf("приёмник предлагает холст %dx%d@%dfps; кодируем %dx%d@%dfps, битрейт %d кбит/с",
			p.Width, p.Height, p.FPS, width, height, p.FPS, bitrate)
		useFFmpeg, mode, err := parseEncoder(o.encoder)
		if err != nil {
			return airplay.VideoPreparationResult{}, err
		}
		var startSource func(context.Context) (airplay.VideoSource, error)
		if useFFmpeg {
			capture := ffmpeg.Config{
				FFmpegPath:   o.ffmpegPath,
				Width:        width,
				Height:       height,
				FPS:          p.FPS,
				BitrateKbps:  o.bitrate,
				DisplayIndex: o.display,
				ShowCursor:   !o.noCursor,
				Logf:         log.Printf,
				Debugf:       debugf(o.verbose),
			}
			startSource = func(c context.Context) (airplay.VideoSource, error) {
				return startVideoSource(c, capture)
			}
		} else {
			// Программный кодер известен только после его запуска: хук снижает
			// размер и частоту кадров (A1), если пользователь не задал их флагами.
			chooser := newProfileChooser(width, height, p.FPS, o, log.Printf)
			capture := native.Config{
				Mode:          mode,
				Display:       o.display,
				Width:         width,
				Height:        height,
				FPS:           p.FPS,
				BitrateKbps:   o.bitrate,
				ShowCursor:    !o.noCursor,
				ForceCPU:      o.noGPUConvert,
				ChooseProfile: chooser.choose,
				Logf:          log.Printf,
				Debugf:        debugf(o.verbose),
			}
			startSource = func(c context.Context) (airplay.VideoSource, error) {
				return startNativeSource(c, capture)
			}
		}
		s, err := startSource(ctx)
		if err != nil {
			logCaptureDiagnostics(log.Printf, err, o.display)
			return airplay.VideoPreparationResult{}, captureError{err}
		}
		// Первый запуск проверяется сразу (ошибки кодера и захвата нужны до SETUP);
		// потом источник перезапускается сам, если он завершится (потеря
		// рабочего стола при блокировке экрана или UAC) или первый кадр успеет устареть.
		primed, err := primeSource(ctx, s)
		if err != nil {
			_ = s.Close()
			logCaptureDiagnostics(log.Printf, err, o.display)
			return airplay.VideoPreparationResult{}, captureError{err}
		}
		newGPUEnv().forgetAttempts() // захват открылся: прежние неудачные предпочтения неактуальны
		video = ffmpeg.Supervise(ctx, ffmpeg.SuperviseConfig{
			Start:       startSource,
			Logf:        log.Printf,
			MaxStartAge: time.Second,
			Fatal:       isFatalCaptureError,
		}, primed)
		log.Println("захват экрана запущен, получены SPS/PPS/IDR")
		return airplay.VideoPreparationResult{}, nil
	}
	retrySetup := func(mix bool) (*airplay.MirrorSession, error) {
		mixOverride = &mix
		useUxPlay = false // повтор идёт общим путём: ключ задан явно (mix)
		if err := reconnect(); err != nil {
			return nil, err
		}
		// Сопряжение без PIN одноразовое и не сохраняется: его повторяем заново;
		// PIN-сопряжение сохранено, достаточно pair-verify по ключам.
		if saved := store.Lookup(info.DeviceID); saved != nil && saved.HasPairingCredentials() {
			if err := client.RestorePairingCredentials(saved); err != nil {
				return nil, err
			}
			if err := client.PairVerify(ctx); err != nil {
				return nil, fmt.Errorf("pair-verify при повторе: %w", err)
			}
		} else if err := client.Pair(ctx, ""); err != nil {
			return nil, fmt.Errorf("сопряжение при повторе: %w", err)
		}
		if err := setupFairPlay(); err != nil {
			return nil, err
		}
		return client.SetupMirrorWithVideoProfilePreparation(ctx, streamCfg, prepare)
	}
	session, err := client.SetupMirrorWithVideoProfilePreparation(ctx, streamCfg, prepare)
	for attempt := 0; errors.Is(err, airplay.ErrCredentialsRequired) && attempt < 3; attempt++ {
		if o.pinPrompt != nil {
			p, perr := o.pinPrompt(ctx, attempt > 0)
			if perr != nil {
				return fmt.Errorf("ввод кода: %w", perr)
			}
			pin = strings.TrimSpace(p)
		} else {
			fmt.Printf("\nПриёмник %q запросил код/пароль. Введите: ", info.Name)
			line, _ := console.ReadString('\n')
			pin = strings.TrimSpace(line)
		}
		if pin == "" {
			return errors.New("код не может быть пустым")
		}
		client.SetPassword(pin)
		session, err = client.SetupMirrorWithVideoProfilePreparation(ctx, streamCfg, prepare)
		if o.pinPrompt == nil {
			break // консоль: одна попытка, как раньше
		}
	}
	if err != nil && o.compat != "uxplay" && client.PairingProtocol() == airplay.PairingProtocolRaw && isHTTPStatus(err, 400) {
		// Режимы auto и generic: UxPlay с битом 27 в features по /info не отличить
		// от приёмников, которым нужен ключ без смешивания (в том числе Apple TV 3
		// со стеком 220.68, который auto ведёт как UxPlay), поэтому один раз
		// повторяем с обратным режимом на новом соединении (ключи уже сохранены).
		mix := !client.FairPlayKeyMixed()
		action := "не смешивать"
		if mix {
			action = "смешать"
		}
		log.Printf("SETUP отклонён (%v); пробуем %s ключ FairPlay с секретом pair-verify на новом соединении", err, action)
		session, err = retrySetup(mix)
	}
	if err != nil {
		return fmt.Errorf("SETUP зеркалирования: %w", err)
	}
	if video == nil {
		return errors.New("SETUP завершён, но видеоисточник не запущен")
	}
	defer session.Close()
	log.Printf("сессия зеркалирования готова (порт данных: %d)", session.DataPort)

	go func() {
		<-ctx.Done()
		_ = video.Close()
		session.Close()
	}()

	// 6. Звук (выключается флагом --no-audio).
	if !o.noAudio && session.HasAudio() {
		ac, aerr := airplay.StartAudioCaptureWithOptions(ctx, session.AudioCodec(), airplay.AudioCaptureOptions{})
		if aerr != nil {
			log.Printf("предупреждение: захват звука не запущен: %v (продолжаем без звука)", aerr)
		} else {
			defer ac.Stop()
			go func() {
				if err := session.StreamAudio(ctx, ac, session.AudioStream()); err != nil && ctx.Err() == nil {
					log.Printf("ошибка передачи звука: %v", err)
					notifyAudioLost(o.onNotice)
				}
			}()
			log.Println("передача звука запущена")
		}
	} else if o.noAudio {
		log.Println("звук выключен (--no-audio)")
	} else {
		log.Println("звук не передаётся: приёмник не выдал аудио-порты")
	}

	// 7. Поток видео.
	log.Println("передача видео началась (Ctrl+C — остановить)")
	rememberReceiver(o.lastReceiverFile, host, port, advert, info)
	if o.onStreaming != nil {
		o.onStreaming()
	}
	if err := session.StreamFrames(ctx, video, 0); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("передача видео: %w", err)
	}
	return nil
}

// captureError помечает ошибку запуска захвата экрана и кодера (текст прежний):
// режим трея по метке показывает пользователю подходящее объяснение.
type captureError struct{ err error }

func (e captureError) Error() string { return e.err.Error() }
func (e captureError) Unwrap() error { return e.err }

// isHTTPStatus сообщает, завершился ли запрос ответом приёмника с указанным кодом.
func isHTTPStatus(err error, code int) bool {
	var statusErr *airplay.HTTPStatusError
	return errors.As(err, &statusErr) && statusErr.StatusCode == code
}

// primeSource дожидается первого кадра (SPS+PPS+IDR), чтобы ошибки кодера и
// захвата экрана обнаружились до SETUP, а не после. Кадр возвращается первым при чтении.
func primeSource(ctx context.Context, s airplay.VideoSource) (airplay.VideoSource, error) {
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	first, err := s.ReadAccessUnit(pctx)
	if err != nil {
		if pctx.Err() != nil && ctx.Err() == nil {
			return nil, errors.New("источник видео не выдал первый кадр за 15 с")
		}
		return nil, fmt.Errorf("ожидание первого кадра: %w", err)
	}
	if !hasNAL(first.AnnexB, 7) || !hasNAL(first.AnnexB, 8) || !hasNAL(first.AnnexB, 5) {
		return nil, errors.New("первый кадр источника видео не содержит SPS, PPS и IDR")
	}
	return &primedSource{VideoSource: s, first: &first}, nil
}

// primedSource отдаёт заранее прочитанный кадр, затем читает из источника.
type primedSource struct {
	airplay.VideoSource
	first *airplay.VideoAccessUnit
}

func (p *primedSource) ReadAccessUnit(ctx context.Context) (airplay.VideoAccessUnit, error) {
	if p.first != nil {
		au := *p.first
		p.first = nil
		return au, nil
	}
	return p.VideoSource.ReadAccessUnit(ctx)
}

// hasNAL сообщает, есть ли в Annex-B данных NAL заданного типа.
func hasNAL(data []byte, typ byte) bool {
	for i := 0; i+3 < len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 && data[i+3]&0x1f == typ {
			return true
		}
	}
	return false
}

func logReceiverInfo(info *airplay.ReceiverInfo) {
	log.Printf("/info: имя=%q модель=%q deviceID=%s srcvers=%s features=0x%x statusFlags=0x%x ключ(pk)=%s",
		info.Name, info.Model, info.DeviceID, info.SourceVersion, info.Features, info.StatusFlags, presence(len(info.PK) > 0))
	if len(info.FeaturesEx) > 0 {
		log.Printf("/info: featuresEx=%x", []byte(info.FeaturesEx))
	}
	if len(info.Displays) == 0 {
		log.Println("/info: displays отсутствуют (приёмник не сообщает размер и maxFPS до SETUP)")
	}
	for i, d := range info.Displays {
		log.Printf("/info: display[%d]: %vx%v (пиксели %vx%v, максимум %vx%v) maxFPS=%v",
			i, d.Width, d.Height, d.WidthPixels, d.HeightPixels, d.WidthPixelsMax, d.HeightPixelsMax, d.MaxFPS)
	}
	log.Printf("/info: требуется пароль=%t, PIN-сопряжение=%t, legacy-сопряжение=%t",
		info.RequiresPassword(), info.RequiresPINPairing(), info.PrefersLegacyPairing())
}

func presence(ok bool) string {
	if ok {
		return "есть"
	}
	return "нет"
}

func pairingProtocolName(p airplay.PairingProtocol) string {
	switch p {
	case airplay.PairingProtocolRaw:
		return "raw (legacy, UxPlay)"
	case airplay.PairingProtocolHAP:
		return "HAP"
	default:
		return "неизвестен"
	}
}

func debugf(verbose bool) func(string, ...any) {
	if !verbose {
		return nil
	}
	return log.Printf
}

func setupLogging(verbose bool) {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.SetPrefix("")
	airplay.SetDebugMode(verbose)
}

// defaultCredentialsFile — %APPDATA%\AirSpoon\credentials.json.
func defaultCredentialsFile() string {
	dir := os.Getenv("APPDATA")
	if dir == "" {
		if d, err := os.UserConfigDir(); err == nil {
			dir = d
		} else {
			dir = "."
		}
	}
	return filepath.Join(dir, "AirSpoon", "credentials.json")
}
