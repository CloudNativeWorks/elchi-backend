# GSLB Metrikleri Dokümantasyonu

Bu dokümantasyon, GSLB (Global Server Load Balancing) sisteminin Prometheus metriklerini detaylı olarak açıklar.

---

## IP Sağlık Durumu Metrikleri

### `elchi_gslb_total_ips`
**Tip:** Gauge
**Açıklama:** Sistemdeki toplam IP sayısı (tüm GSLB kayıtlarındaki IP'ler)

```
elchi_gslb_total_ips{controller="pehlivan.local"} 745.000000
```

**Yorumlama:**
- MongoDB `gslb_ip_health` koleksiyonundaki toplam kayıt sayısı
- Bu değer sabit kalmalı (IP eklenmedikçe/silinmedikçe)

---

### `elchi_gslb_healthy_ips`
**Tip:** Gauge
**Açıklama:** Sağlıklı (PASSING veya WARNING) durumda olan IP sayısı

```
elchi_gslb_healthy_ips{controller="pehlivan.local",state="passing"} 115.000000
```

**Yorumlama:**
- DNS sorgularına dahil edilen IP'ler
- PASSING + WARNING = Sağlıklı (DNS'e dahil)
- Düşük değer = Çoğu IP'nin probe'lardan geçemediğini gösterir

---

### `elchi_gslb_warning_ips`
**Tip:** Gauge
**Açıklama:** WARNING durumunda olan IP sayısı (geçici başarısızlık)

```
elchi_gslb_warning_ips{controller="pehlivan.local",state="warning"} 0.000000
```

**Yorumlama:**
- 1-2 ardışık başarısızlık yaşayan IP'ler
- Hala DNS'e dahil edilir
- 0 olması: IP'ler ya PASSING ya da direkt CRITICAL'a geçiyor
- WARNING → CRITICAL geçişi çok hızlı olduğunda bu değer düşük kalır

---

### `elchi_gslb_critical_ips`
**Tip:** Gauge
**Açıklama:** CRITICAL durumunda olan IP sayısı (sağlıksız)

```
elchi_gslb_critical_ips{controller="pehlivan.local",state="critical"} 630.000000
```

**Yorumlama:**
- DNS sorgularından **çıkarılan** IP'ler
- `critical_threshold` (varsayılan: 3) ardışık başarısızlık sonrası
- Yüksek değer = Çoğu IP'nin probe'lardan geçemediğini gösterir
- **Test ortamında normal** (test IP'leri gerçek health endpoint'i olmayan public IP'ler)

---

### `elchi_gslb_backoff_active_ips`
**Tip:** Gauge
**Açıklama:** Backoff (geri çekilme) modunda olan IP sayısı

```
elchi_gslb_backoff_active_ips{controller="pehlivan.local"} 628.000000
```

**Yorumlama:**
- CRITICAL IP'ler graduated backoff ile probe ediliyor
- Backoff süresi: 10s → 20s → 30s → 50s → 80s → 120s (max)
- `backoff_until > now()` olan IP sayısı
- CRITICAL IP'lerden biraz az olabilir (bazıları backoff süresi dolmuş)

---

## ⏱️ Time Wheel Scheduler Metrikleri

### `elchi_gslb_timewheel_current_load`
**Tip:** Gauge
**Açıklama:** Time Wheel'da şu an schedule edilmiş görev sayısı

```
elchi_gslb_timewheel_current_load{controller="pehlivan.local"} 738.000000
```

**Yorumlama:**
- `total_ips`'e yakın olmalı (her IP bir görev)
- Fark = in-flight probes (şu an çalışan)
- 745 - 738 = 7 IP şu an probe ediliyor

---

### `elchi_gslb_timewheel_current_slot`
**Tip:** Gauge
**Açıklama:** Time Wheel'ın şu anki slot indeksi (0-511)

```
elchi_gslb_timewheel_current_slot{controller="pehlivan.local"} 192.000000
```

**Yorumlama:**
- Her saniye 1 artar
- 512'ye ulaşınca 0'a döner (~8.5 dakikada bir tur)
- Debugging için kullanılır

---

### `elchi_gslb_timewheel_scheduled_total`
**Tip:** Counter
**Açıklama:** Toplam schedule edilen görev sayısı (kümülatif)

```
elchi_gslb_timewheel_scheduled_total{controller="pehlivan.local"} 169723.000000
```

**Yorumlama:**
- Her Schedule() çağrısında artar
- İlk yükleme + tüm reschedule'lar dahil
- `scheduled > executed` normal (retry'lar dahil)

---

### `elchi_gslb_timewheel_executed_total`
**Tip:** Counter
**Açıklama:** Toplam çalıştırılan probe sayısı (kümülatif)

```
elchi_gslb_timewheel_executed_total{controller="pehlivan.local"} 137319.000000
```

**Yorumlama:**
- Worker pool'a gönderilen probe sayısı
- `scheduled - executed` farkı = bekleyen veya atlanan görevler

---

## Probe Sonuç Metrikleri

### `elchi_gslb_probes_total`
**Tip:** Counter
**Açıklama:** Toplam probe sayısı (başarılı/başarısız)

```
elchi_gslb_probes_total{controller="pehlivan.local",result="success"} 50715.000000
elchi_gslb_probes_total{controller="pehlivan.local",result="failure"} 106943.000000
```

**Yorumlama:**
- `success`: HTTP 200, TCP bağlantı başarılı vb.
- `failure`: Timeout, connection refused, status mismatch vb.
- Toplam: 50715 + 106943 = 157658 probe

---

### `elchi_gslb_probe_success_rate_percent`
**Tip:** Gauge
**Açıklama:** Probe başarı oranı (%)

```
elchi_gslb_probe_success_rate_percent{controller="pehlivan.local"} 33.600073
```

**Yorumlama:**
- Hesaplama: `success / (success + failure) * 100`
- 33.6% = 50715 / 157658 * 100
- **Test ortamında düşük normal** (test IP'leri gerçek endpoint değil)
- **Production'da**: %95+ olmalı

---

### `elchi_gslb_probe_errors_total`
**Tip:** Counter
**Açıklama:** Hata türüne göre probe başarısızlıkları

```
elchi_gslb_probe_errors_total{controller="pehlivan.local",error_type="http_status_mismatch"} 51949.000000
elchi_gslb_probe_errors_total{controller="pehlivan.local",error_type="timeout"} 29215.000000
elchi_gslb_probe_errors_total{controller="pehlivan.local",error_type="url_error"} 25747.000000
elchi_gslb_probe_errors_total{controller="pehlivan.local",error_type="connection_reset"} 32.000000
```

**Hata Türleri:**

| Hata Türü | Açıklama | Olası Neden |
|-----------|----------|-------------|
| `http_status_mismatch` | Beklenen HTTP kodu gelmedi | 200 beklendi, 404/503 geldi |
| `timeout` | Probe süresi doldu | Server yavaş veya ulaşılamaz |
| `url_error` | URL/bağlantı hatası | DNS çözümleme, SSL hatası |
| `connection_reset` | Bağlantı resetlendi | Server bağlantıyı kapattı |
| `connection_refused` | Bağlantı reddedildi | Port kapalı |
| `dns_not_found` | DNS çözümleme başarısız | Domain bulunamadı |
| `tls_certificate` | SSL sertifika hatası | Geçersiz/süresi dolmuş sertifika |

---

## ⏱️ Latency Metrikleri

### `elchi_gslb_probe_latency_avg_seconds`
**Tip:** Gauge
**Açıklama:** Ortalama probe süresi (saniye)

```
elchi_gslb_probe_latency_avg_seconds{controller="pehlivan.local"} 0.512839
```

**Yorumlama:**
- Tüm probe'ların ortalama yanıt süresi
- 0.5s = 500ms ortalama
- Yüksek değer = Network yavaş veya server'lar yavaş yanıt veriyor

---

### `elchi_gslb_probe_latency_min_seconds`
**Tip:** Gauge
**Açıklama:** Minimum probe süresi (saniye)

```
elchi_gslb_probe_latency_min_seconds{controller="pehlivan.local"} 0.006946
```

**Yorumlama:**
- En hızlı probe süresi
- ~7ms = Muhtemelen yakın lokasyondaki bir CDN IP'si

---

### `elchi_gslb_probe_latency_max_seconds`
**Tip:** Gauge
**Açıklama:** Maksimum probe süresi (saniye)

```
elchi_gslb_probe_latency_max_seconds{controller="pehlivan.local"} 3.015953
```

**Yorumlama:**
- En yavaş probe süresi
- ~3s = Timeout'a yakın (timeout: 2-3s genelde)
- Yüksek latency'li probe'lar genelde FAIL olur

---

## 👷 Worker Pool Metrikleri

### `elchi_gslb_workers_current`
**Tip:** Gauge
**Açıklama:** Şu anki aktif worker (goroutine) sayısı

```
elchi_gslb_workers_current{controller="pehlivan.local"} 100.000000
```

**Yorumlama:**
- Paralel probe çalıştıran worker sayısı
- Auto-scaling: min 50 → max 500 (CPU'ya göre)
- 100 worker = Yeterli kapasite

---

### `elchi_gslb_workers_queue_depth`
**Tip:** Gauge
**Açıklama:** Worker queue'da bekleyen görev sayısı

```
elchi_gslb_workers_queue_depth{controller="pehlivan.local"} 0.000000
```

**Yorumlama:**
- 0 = Tüm görevler anında işleniyor (ideal)
- Yüksek değer = Worker'lar yetişemiyor, scale-up gerekli
- Sürekli yüksekse: Worker sayısını artır

---

## Result Queue Metrikleri

### `elchi_gslb_result_queue_depth`
**Tip:** Gauge
**Açıklama:** Result queue'da bekleyen sonuç sayısı

```
elchi_gslb_result_queue_depth{controller="pehlivan.local"} 0.000000
```

**Yorumlama:**
- Worker'ların ürettiği sonuçların bekleyen sayısı
- 0 = Result processor'lar anında işliyor (ideal)
- Yüksek değer = Result işleme yetişemiyor

---

### `elchi_gslb_result_queue_capacity_pct`
**Tip:** Gauge
**Açıklama:** Result queue doluluk oranı (%)

```
elchi_gslb_result_queue_capacity_pct{controller="pehlivan.local"} 0.000000
```

**Yorumlama:**
- 0% = Queue boş (ideal)
- >70% = Uyarı seviyesi
- >90% = Kritik, sonuçlar düşebilir

---

## 💾 Write Buffer Metrikleri

### `elchi_gslb_write_buffer_size`
**Tip:** Gauge
**Açıklama:** Write buffer'da bekleyen güncelleme sayısı

```
elchi_gslb_write_buffer_size{controller="pehlivan.local"} 0.000000
```

**Yorumlama:**
- MongoDB'ye yazılmayı bekleyen state güncellemeleri
- 0 = Tüm güncellemeler yazıldı
- Yüksek değer normal (batching için birikir)

---

### `elchi_gslb_write_buffer_capacity_pct`
**Tip:** Gauge
**Açıklama:** Write buffer doluluk oranı (%)

```
elchi_gslb_write_buffer_capacity_pct{controller="pehlivan.local"} 0.000000
```

**Yorumlama:**
- 0% = Buffer boş
- >80% = Otomatik flush tetiklenir
- 100% = Buffer dolu, yeni güncellemeler bekler

---

### `elchi_gslb_write_buffer_flush_total`
**Tip:** Counter
**Açıklama:** Toplam flush (MongoDB yazma) sayısı

```
elchi_gslb_write_buffer_flush_total{controller="pehlivan.local"} 66.000000
```

**Yorumlama:**
- Her 5 saniyede veya buffer dolduğunda flush olur
- 66 flush = Sistem bir süredir çalışıyor

---

### `elchi_gslb_write_buffer_updates_total`
**Tip:** Counter
**Açıklama:** Toplam güncelleme (state change) sayısı

```
elchi_gslb_write_buffer_updates_total{controller="pehlivan.local"} 88.000000
```

**Yorumlama:**
- State değişikliği olan IP güncellemeleri
- Düşük sayı = Çoğu IP'nin state'i değişmedi (CRITICAL'da kaldı)
- `updates / flush` = Ortalama batch boyutu (88/66 ≈ 1.3)

---

### `elchi_gslb_write_buffer_flush_errors_total`
**Tip:** Counter
**Açıklama:** Başarısız flush sayısı

```
elchi_gslb_write_buffer_flush_errors_total{controller="pehlivan.local"} 0.000000
```

**Yorumlama:**
- 0 = Tüm MongoDB yazmaları başarılı (ideal)
- >0 = MongoDB bağlantı sorunu veya yazma hatası

---

### `elchi_gslb_write_buffer_avg_flush_duration_seconds`
**Tip:** Gauge
**Açıklama:** Ortalama flush süresi (saniye)

```
elchi_gslb_write_buffer_avg_flush_duration_seconds{controller="pehlivan.local"} 0.090973
```

**Yorumlama:**
- ~91ms = MongoDB batch write süresi
- Düşük = MongoDB performansı iyi
- Yüksek (>500ms) = MongoDB yavaş veya network latency

---

## 🔢 Shard Metrikleri

### `elchi_gslb_owned_shards`
**Tip:** Gauge
**Açıklama:** Bu controller'ın sahip olduğu shard sayısı

```
elchi_gslb_owned_shards{controller="pehlivan.local"} 1024.000000
```

**Yorumlama:**
- Toplam 1024 logical shard (128 top-level × 8 sub-shard)
- Tek controller = 1024 (tüm shard'lar)
- Multi-controller = Shard'lar eşit dağılır (1024 / N controller)

---

## 📈 Özet Dashboard Önerileri

### Kritik Alarm Eşikleri

| Metrik | Uyarı | Kritik |
|--------|-------|--------|
| `probe_success_rate_percent` | <90% | <80% |
| `result_queue_capacity_pct` | >50% | >80% |
| `workers_queue_depth` | >100 | >500 |
| `write_buffer_flush_errors_total` | >0 | >10 |
| `critical_ips / total_ips` | >10% | >30% |

### Performans İzleme

| Metrik | İdeal Değer |
|--------|-------------|
| `probe_latency_avg_seconds` | <0.5s |
| `write_buffer_avg_flush_duration_seconds` | <0.1s |
| `timewheel_current_load` | ≈ `total_ips` |
| `result_queue_depth` | 0 |
| `workers_queue_depth` | 0 |

---

## Troubleshooting

### Düşük Success Rate
1. `probe_errors_total` kontrol et - hangi hata türü dominant?
2. `http_status_mismatch` yüksekse: Health endpoint'ler doğru mu?
3. `timeout` yüksekse: Timeout değerini artır veya network kontrol et

### Yüksek Queue Depth
1. `workers_current` kontrol et - yeterli worker var mı?
2. CPU kullanımı kontrol et - worker scale-up gerekli mi?
3. `probe_latency_avg` kontrol et - probe'lar çok mu yavaş?

### Write Buffer Sorunları
1. `flush_errors_total` >0 ise: MongoDB bağlantısı kontrol et
2. `avg_flush_duration` >500ms ise: MongoDB performansı kontrol et
3. `capacity_pct` sürekli yüksekse: Flush interval'i azalt
