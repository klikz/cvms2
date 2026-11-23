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
4. Clientda **Kamera qo'shish**: tarmoqdan qidirish (ONVIF) yoki IP / RTSP havola orqali.
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
  (`stream_idle_seconds` dan keyin to'xtaydi), bitta oqim ko'p tomoshabinga umumiy.
- **H.264**: o'zgartirishsiz uzatiladi.
- **H.265**: ffmpeg bilan H.264 ga o'giriladi — serverda (default) yoki clientda
  (`transcode_mode: client`). GPU encoder (NVENC/QSV/VAAPI/AMF) avtomatik tanlanadi.
- Brauzer oynasi yashirilsa yoki `viewer_idle_minutes` davomida harakat bo'lmasa oqimlar to'xtaydi.

## Xavfsizlik

- Parollar bcrypt, login urinishlari cheklangan (5 ta xato → 15 daqiqa blok), sessiya harakatsizlikda tugaydi.
- Client config'idagi kamera parollari AES-256-GCM bilan shifrlangan (`client.key` alohida faylda).
- Audit jurnali: kirishlar, xato kirishlar, ko'rishlar, admin o'zgarishlari (`audit_retention_days`).
- TLS o'rnatilmagan. Internetga chiqarishda server oldiga reverse proxy (nginx/Caddy) qo'yib HTTPS
  ishlating va `trust_proxy: true` qiling; client'da `https://...` manzil kiritiladi.
