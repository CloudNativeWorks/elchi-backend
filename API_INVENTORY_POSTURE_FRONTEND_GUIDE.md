# API Inventory — Posture & Maturity (Frontend Guide)

Collector'ın iki-eksenli skorlaması (THREAT `max_risk_score` ⟂ EXPOSURE
`max_posture_score`) ve route-aware `confirmed` değişikliklerine bağlı yeni
backend yetenekleri. Hepsi **additive** — mevcut inventory response'ları
değişmedi, yeni alanlar/endpoint'ler eklendi.

Çözdüğü asıl dert: inventory skorları **monotonik** (`$max` — sadece yükselir),
bir endpoint remediate edilse bile (auth eklendi, TLS düzeltildi) **sonsuza dek
kırmızı** kalıyor. Aşağıdaki "current vs ever" bunu çözer.

İki kavram:
- **`_ever`** — inventory dokümanındaki kümülatif/tarihsel değer ("bugüne dek görülen en kötü").
- **`_current`** — ClickHouse'tan pencereli ("son N günde HÂLÂ kötü mü?").

> Ortak: tüm endpoint'ler JWT gerektirir, `project` query param zorunludur,
> hata kodları diğer inventory endpoint'leriyle aynı (400/401/403/503/502).

---

## 1) Skor re-baseline (collector skorlaması değişti → eski skorlar şişik)

### 1a. Tekil reset davranış değişikliği
`POST /api/v3/inventory/:id/reset` artık **her iki ekseni** sıfırlıyor
(`max_risk_score` + `max_posture_score`). Önceden posture ekseni atlanıyordu.
API şekli değişmedi — sadece davranış düzeldi. UI tarafında aksiyon yok.

### 1b. Bulk re-baseline (yeni, Admin/Owner)
`POST /api/v3/inventory/rebaseline-scores?project=<id>`

Proje genelinde `max_risk_score` + `max_posture_score`'u 0'lar; collector sonraki
event'ten itibaren doğru değeri biriktirir. "Skorlar şişik görünüyor /
collector güncellendi" durumunda fleet-wide tek-tık düzeltme.

**Body:** yok · **Response 200:**
```jsonc
{
  "message": "inventory risk/posture scores reset; collector re-accumulates from the next event",
  "matched_count": 1423,
  "modified_count": 1423
}
```
**403:** Admin/Owner değilse.

**UI:** Inventory ayarlar/aksiyon menüsünde "Reset risk scores (project)" butonu
→ onay modalı ("skorlar sıfırlanır, trafik geldikçe yeniden hesaplanır") → POST →
başarıda listeyi refetch. Sadece Admin/Owner'a göster (`isOwner` boolean tercih
et, role string'i değil).

> ⚠️ Reset sonrası az-trafikli endpoint'ler bir süre `max_risk_score: 0` görünür
> (sonraki event'e kadar). Bu beklenen; asıl "doğru current" cevabı için
> aşağıdaki **current posture** (bölüm 3) kullanılır.

---

## 2) Olgunluk eşiği — `min_seen` (FP azaltma, opt-in)

Route-aware `confirmed` tek event'le promote ettiği için tek-atış scanner'lar
temiz kataloğa sızabilir. `min_seen` ile az-gözlemli operasyonlar elenir.

**Opt-in** (default kapalı — gönderilmezse davranış aynı). Şu üç görünüme eklendi:

| Endpoint | Eşik alanı |
|---|---|
| `GET /api/v3/inventory?min_seen=N` | `seen_count >= N` (per-op) |
| `GET /api/v3/inventory/listeners?min_seen=N` | pre-group `seen_count >= N` |
| `GET /api/v3/inventory/operations?min_seen=N` | post-group `total_seen >= N` |

Attack-surface görünümü (`/attack-surface`) **değişmedi** (zaten gürültü görünümü).

**UI önerisi:** "Sadece olgun endpoint'ler" toggle'ı (örn `min_seen=5`). Hard
gizleme yerine ileride "emerging/yeni keşfedilen" rozeti ile **ayırmak** tercih
edilebilir (bilgi kaybı olmaz) — backend hard filtre olarak hazır; ayırma
istenirse `min_seen` göndermeden tüm listeyi çekip UI'da `seen_count < N`
olanları ayrı grupla.

---

## 3) Current posture — TEKİL endpoint detayı (Faz 1)

`GET /api/v3/inventory/:id/current-posture?window_days=N`

- `window_days` opsiyonel, default **7**, max **7** (raw retention sınırı — current ham event'lerden okunur).
- "Bu operasyon son N günde hâlâ kötü mü?" — detay sayfasının üstünde
  "current vs ever" rozeti için.

**Response 200:**
```jsonc
{
  "current_available": true,        // false → ClickHouse yok/sorgu patladı
  "dormant": false,                  // true → CH var ama pencerede trafik yok
  "window_days": 7,
  "current": {                       // dormant veya unavailable ise null
    "active": true,
    "window_days": 7,
    "max_risk_score": 8,             // THREAT, pencereli — ham event'ten TAM anahtarla (host dahil) çekilir; host-precise + sampling-safe (riskli event'ler hiç düşmez)
    "risk_flags": ["scanner_probe"], // pencerede HÂLÂ görülen flag'ler (raw, ≤7g)
    "pii_categories": [],
    "auth_observed": true,           // pencerede ≥1 kimlikli istek
    "noauth_observed": false,        // pencerede ≥1 kimliksiz istek (posture sinyali)
    "event_count": 1234,
    "last_active": "2026-05-22T09:14:00Z"
  },
  "ever": {                          // inventory dokümanından (monotonik)
    "max_risk_score": 26,
    "max_posture_score": 14
  },
  "posture_current_available": false // current EXPOSURE skoru henüz yok (aşağıya bak)
}
```

**Üç durum / UI:**
1. `current` dolu (`active:true`) → **current'ı birincil göster**, `ever`'i
   "historically: 26" olarak yanına koy. `current.max_risk_score < ever.max_risk_score`
   ise "improved/remediated" yeşil işareti güçlü bir sinyaldir.
2. `dormant:true` → "Dormant — son aktivite: doc'un `last_seen`'i, historical max:
   `ever.max_risk_score`". (current null; "temiz çünkü sessiz" ≠ "temiz çünkü düzeltildi".)
3. `current_available:false` → ClickHouse kapalı; sadece `ever` göster, "current
   unavailable" notu. (200 döner, bloklamaz.)

> **`posture_current_available: false` ne demek:** Current THREAT ekseni hazır,
> ama current EXPOSURE skoru henüz yok — collector `posture_score`'u ClickHouse'a
> yazmaya başlayınca otomatik `true` olur ve `current` içine `max_posture_score`
> eklenir. UI'ı buna göre kur: `posture_current_available` true olunca current
> posture rozetini göster, false iken posture'ı sadece `ever.max_posture_score`
> üzerinden göster. **Bu alanı şimdiden oku** ki kolon gelince UI'da değişiklik
> gerekmesin.

---

## 4) Current posture — LİSTE enrichment (Faz 2)

Katalog listesinde her satıra current THREAT skorunu ekler — "katalog kırmızı
görünüyor" sorununun asıl çözümü.

**Opt-in:** `?with_current=true` (+ opsiyonel `?current_window_days=N`, default 7).
Şu iki görünümde:
- `GET /api/v3/inventory?with_current=true` (flat — satır = operasyon)
- `GET /api/v3/inventory/operations?with_current=true` (path-grouped)

**Eklenen alanlar:**

Her satırda (flat) / her `operations[]` girişinde (grouped) **ve** operations
path-row'unda:
```jsonc
{
  // ...mevcut alanlar...
  "current_max_risk": 8,      // number | null (null = dormant)
  "current_dormant": false    // true = pencerede trafik yok
}
```
operations görünümünde path satırı = aktif operasyonlarının max'ı; tüm
operasyonlar dormant ise satır da dormant.

**Top-level (response kökünde):**
```jsonc
{
  "data": [ ... ],
  "current_available": true,         // false → CH yok/patladı (current_* alanları yok say)
  "posture_current_available": false // posture-current henüz yok
  // ...pagination alanları...
}
```

**UI önerisi (önemli):**
- Katalog görünümlerini current-by-default yapmak için **`with_current=true`
  gönder.** (Backend opt-in; default davranışı bozmamak için. UI flag'i geçince
  current gelir.)
- Risk kolonu: `current_available && !current_dormant` → `current_max_risk`'i
  birincil rozet yap; `current_dormant` → gri/soluk + tooltip "dormant ·
  historical: `max_risk_score` (ever)"; `current_available:false` → eskisi gibi
  `max_risk_score` (ever) göster.
- "Sort by current risk" UI'da YOK (current ClickHouse-türevi, Mongo sıralaması
  `_ever` üzerinden). İhtiyaç olursa backend ekibine bildir (CH-side
  aggregate-then-paginate gerekir).

---

## Özet — alan referansı

| Alan | Nerede | Tip | Anlam |
|---|---|---|---|
| `max_risk_score` | her zaman (ever) | number | tarihsel max THREAT |
| `max_posture_score` | her zaman (ever) | number | tarihsel max EXPOSURE |
| `current_max_risk` | liste, `with_current=true` | number\|null | pencereli THREAT (null=dormant) |
| `current_dormant` | liste, `with_current=true` | bool | pencerede trafik yok |
| `current` | `/:id/current-posture` | obj\|null | tekil current snapshot |
| `current_available` | her ikisi | bool | CH erişilebilir mi |
| `posture_current_available` | her ikisi | bool | current EXPOSURE hazır mı (şimdilik false) |

## Yapılması gerekenler (UI)
1. Detay sayfası: `/:id/current-posture` çağır → current/ever/dormant durumlarını render et.
2. Katalog listeleri: `with_current=true` geç → risk rozetini current'a çevir, dormant'ı soluk göster.
3. Admin aksiyonu: "Reset risk scores (project)" → `POST /rebaseline-scores`.
4. (Opsiyonel) Olgunluk toggle: `min_seen=5`.
5. **`posture_current_available`'ı şimdiden oku** — collector kolonu gelince posture-current otomatik açılır, UI değişikliği gerekmez.
```
