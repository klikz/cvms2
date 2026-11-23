# CVMS — Central Video Management System

Client mahalliy kameralarni (RTSP / ONVIF) serverga uzatadi. Server barcha clientlarni jamlaydi
va web UI orqali faqat ruxsat etilgan kameralarni jonli ko'rsatadi. Video **saqlanmaydi**.

```
cvms/
  VERSION        umumiy versiya (build fayl nomida ishlatiladi)
  shared/        umumiy Go paketlar + shared/ui (umumiy dizayn, i18n uz/ru/en)
  client/        client dasturi   -> client/builds/
  server/        server dasturi   -> server/builds/
```

## Talablar

- Go 1.24+
- GNU make 4.2+ (Windows: `winget install ezwinports.make`)
- **ffmpeg** (ffprobe bilan) — PATH'da yoki `config.yaml` dagi `ffmpeg_path`.
  Client uchun majburiy; server uchun faqat H.265 kameralarni o'girishda kerak.

## Komandalar (client/ yoki server/ papkasi ichida)

| Komanda | Vazifa |
|---|---|
| `make run` | ishga tushirish (Ubuntu) |
| `make build` | Ubuntu uchun build → `builds/cvms-<app>-<versiya>-linux-amd64` |
| `make runWin` | ishga tushirish (Windows) |
| `make buildWin` | Windows uchun build → `builds/cvms-<app>-<versiya>-windows-amd64.exe` |
| `make test` / `make vet` | testlar / tekshiruv |

Versiyani o'zgartirish: `VERSION` faylini tahrirlang.

## Birinchi ishga tushirish

1. **Server**: ishga tushiring. `config.yaml` va `cvms.db` avtomatik yaratiladi, logda vaqtinchalik
   `admin` paroli chiqadi. `http://<server>:8080` ni oching, kiring va parolni almashtiring.
2. **Client**: ishga tushiring. `config.yaml` va `client.key` yaratiladi, logda UI paroli chiqadi.
   `http://127.0.0.1:8081` ni oching → parolni almashtiring → **Server** bo'limida server manzilini
   kiriting (masalan `http://192.168.1.10:8080`).
3. Serverda **Clientlarni boshqarish** → clientni **Tasdiqlash**.
4. Clientda **Kamera qo'shish**: login/parolni bir marta kiriting, tarmoqdan topilgan (ONVIF) yoki
   IP diapazon (`192.168.1.10-50`) bo'yicha skanerlangan qurilmalarni belgilab, bir bosishda qo'shing.
   ONVIF'siz kameralar RTSP havola orqali qo'shiladi.
5. Serverda **Foydalanuvchilar** → oddiy userlarga qaysi clientlarni ko'rishni belgilang.

Parolni unutsangiz: `cvms-server -reset-admin`, `cvms-client -reset-password`.

## Servis sifatida (Windows Service / systemd)

```
cvms-server install     # keyin: start | stop | restart | status | uninstall
cvms-client install
```
Ubuntu'da `sudo` bilan. Config fayllar exe yonida (yoki `-dir <papka>`).

## Portlar

| | Port | |
|---|---|---|
| Server web/API + client ulanishi | TCP 8080 | `listen` |
| WebRTC media | UDP 8189, TCP 8189 | `webrtc.udp_port`, `webrtc.tcp_port` |
| Client web UI | TCP 8081 (faqat 127.0.0.1) | `ui.listen` |

Client serverga o'zi ulanadi (chiquvchi WebSocket) — client tomonda port ochish shart emas.
Server NAT ortida bo'lsa `webrtc.public_ips` ga tashqi IP'ni yozing. UDP yopiq tarmoqlarda brauzer
avtomatik WebSocket (MSE) orqali ko'rsatishga o'tadi.

## Video yo'li

- Grid: **sub stream**, 2 marta bosilganda: **main stream**. Oqim faqat kimdir ko'rayotganda ishlaydi
  (`stream_idle_seconds`, default 30 s, dan keyin to'xtaydi), bitta oqim ko'p tomoshabinga umumiy.
- **H.264**: o'zgartirishsiz uzatiladi.
- **H.265**: brauzer qo'llasa (Chrome/Edge 136+ GPU bilan, Safari) **o'zgartirishsiz** uzatiladi —
  transcode yo'q. Qo'llamasa H.264 ga o'giriladi: serverda (`h265_fallback: server`, default) yoki
  kamera turgan clientda (`h265_fallback: client`). Transcoder faqat H.264 tomoshabin bo'lgan paytda ishlaydi.
- **GPU**: server va client `hwaccel: auto` bilan bor GPU'ni topadi (NVIDIA CUDA/NVENC, Intel QSV,
  Linux VAAPI, Windows D3D11VA+AMF) va H.265 decode + H.264 encode'ni to'liq GPU'da qiladi.
  Avtoaniqlash haqiqiy sinov kodlash bilan tekshiradi; GPU ishlamasa CPU'ga qaytadi.
  Bir nechta GPU bo'lsa `gpu_device` (indeks yoki `/dev/dri/renderD129`). Tanlangan pipeline server
  sozlamalarida va client bosh sahifasida ko'rinadi.
- Brauzer oynasi yashirilsa yoki `viewer_idle_minutes` davomida harakat bo'lmasa oqimlar to'xtaydi.

## Quvvat hisobi (200–500 kamera)

Server yuklamasi kameralar soniga emas, **bir vaqtda ko'rilayotgan noyob oqimlarga** bog'liq.
10 user × 4×4 grid ≈ 160 sub oqim + bir nechta main.

| | Bitrate (taxminan) | 160 sub + 10 main |
|---|---|---|
| Sub oqim (640×360 … 720p) | 0.5–1 Mbit/s | ~80–160 Mbit/s |
| Main oqim (1080p … 4K) | 2–8 Mbit/s | ~20–80 Mbit/s |

- Trafik ikki marta o'tadi: client → server va server → brauzer. Server uchun 1 Gbit/s tarmoq tavsiya etiladi.
- H.265 to'g'ridan uzatilganda server CPU'si deyarli band bo'lmaydi (faqat paket uzatish).
- H.264 fallback transcode: sub oqim GPU'da ~1–2 %, CPU'da (libx264) bitta yadroning ~5–10 %;
  main 1080p CPU'da ~0.5–1 yadro. Ko'p H.265 kamera va eski brauzerlar bo'lsa GPU yoki `h265_fallback: client`.
- Client: rasm olish `snapshot_concurrency` (default 4) bilan cheklangan; kamera ONVIF orqali
  qo'shilsa HTTP snapshot ishlatiladi (ffmpeg'siz). Client gridi faqat ekranda ko'rinadigan rasmlarni yangilaydi.
- Audit: `audit_views: main` (default) — faqat 2 marta bosib ochilgan kameralar yoziladi
  (`all` — grid kataklari ham, `none` — hech biri).

## Xavfsizlik

- Parollar bcrypt, login urinishlari cheklangan (5 ta xato → 15 daqiqa blok), sessiya harakatsizlikda tugaydi.
- Client config'idagi kamera parollari AES-256-GCM bilan shifrlangan (`client.key` alohida faylda).
- Audit jurnali: kirishlar, xato kirishlar, ko'rishlar, admin o'zgarishlari (`audit_retention_days`).
- TLS o'rnatilmagan. Internetga chiqarishda server oldiga reverse proxy (nginx/Caddy) qo'yib HTTPS
  ishlating va `trust_proxy: true` qiling; client'da `https://...` manzil kiritiladi.
