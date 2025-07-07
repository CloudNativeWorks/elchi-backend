// TRUE

// Alt-Svc for IPs kullanımına izin verir.
envoy.reloadable_features.allow_alt_svc_for_ips

// Boolean değerlerin string'e dönüşümünü düzeltir.
envoy.reloadable_features.boolean_to_string_fix

// WebSocket handshake için protokol değişimini kontrol eder.
envoy.reloadable_features.check_switch_protocol_websocket_handshake

// Bağlantı havuzunu boşta kaldığında siler.
envoy.reloadable_features.conn_pool_delete_when_idle

// Tutarlı başlık doğrulaması yapar.
envoy.reloadable_features.consistent_header_validation

// İşlenmesi ertelenmiş akışları yedekler.
envoy.reloadable_features.defer_processing_backedup_streams

// Karışık şema desteği sağlar.
envoy.reloadable_features.dfp_mixed_scheme

// QUIC istemci UDP mesajlarını engeller.
envoy.reloadable_features.disallow_quic_client_udp_mmsg

// DNS ayrıntılarını kaydeder.
envoy.reloadable_features.dns_details

// NoData veya NoName yanıtlarını başarılı olarak değerlendirir.
envoy.reloadable_features.dns_nodata_noname_is_success

// EAI_AGAIN hatasında DNS yeniden çözümlemesini sağlar.
envoy.reloadable_features.dns_reresolve_on_eai_again

// EDF LB host zamanlayıcı başlangıç hatasını düzeltir.
envoy.reloadable_features.edf_lb_host_scheduler_init_fix

// EDF LB locality zamanlayıcı başlangıç hatasını düzeltir.
envoy.reloadable_features.edf_lb_locality_scheduler_init_fix

// Sıkıştırma bombalarına karşı koruma sağlar.
envoy.reloadable_features.enable_compression_bomb_protection

// Histogramların dahil edilmesine izin verir.
envoy.reloadable_features.enable_include_histograms

// EDS durumu boşaltılırken host'u hariç tutar.
envoy.reloadable_features.exclude_host_in_eds_status_draining

// Dış işlem hatalarını zaman aşımına uğratır.
envoy.reloadable_features.ext_proc_timeout_error

// Güvensiz kabul edilen H3 başlıklarını genişletir.
envoy.reloadable_features.extend_h3_accept_untrusted

// GCP kimlik doğrulama için sabit URL kullanır.
envoy.reloadable_features.gcp_authn_use_fixed_url

// getaddrinfo yeniden deneme sayısını ayarlar.
envoy.reloadable_features.getaddrinfo_num_retries

// gRPC yan akış akış kontrolünü etkinleştirir.
envoy.reloadable_features.grpc_side_stream_flow_control

// HTTP/1 Balsa parser reset gecikmesini ayarlar.
envoy.reloadable_features.http1_balsa_delay_reset

// HTTP/1 Balsa parser'da yalnız başına CR'yi chunk uzantısında engeller.
envoy.reloadable_features.http1_balsa_disallow_lone_cr_in_chunk_extension

// HTTP/1 için Balsa parser kullanımını etkinleştirir.
envoy.reloadable_features.http1_use_balsa_parser

// HTTP/2 host başlığını atar.
envoy.reloadable_features.http2_discard_host_header

// HTTP/2 için OGHTTP2 kullanımını etkinleştirir.
envoy.reloadable_features.http2_use_oghttp2

// HTTP/2 veriler için ziyaretçi kullanımını etkinleştirir.
envoy.reloadable_features.http2_use_visitor_for_data

// HTTP/2 authority doğrulamasını QUICHE ile yapar.
envoy.reloadable_features.http2_validate_authority_with_quiche

// HTTP/3 mutlu gözyaşlarını etkinleştirir.
envoy.reloadable_features.http3_happy_eyeballs

// HTTP/3 boş treylerleri kaldırır.
envoy.reloadable_features.http3_remove_empty_trailers

// HTTP filtresinde yeniden girdi local yanıtı önler.
envoy.reloadable_features.http_filter_avoid_reentrant_local_reply

// HTTP yolundaki parça ile URL reddeder.
envoy.reloadable_features.http_reject_path_with_fragment

// Bağlantı proxy varsayılanını etkinleştirir.
envoy.reloadable_features.http_route_connect_proxy_by_default

// İç otorite başlık doğrulayıcıyı etkinleştirir.
envoy.reloadable_features.internal_authority_header_validator

// JWT kimlik doğrulamasında query parametrelerinden JWT'yi kaldırır.
envoy.reloadable_features.jwt_authn_remove_jwt_from_query_params

// JWT kimlik doğrulamasında URI'yi doğrular.
envoy.reloadable_features.jwt_authn_validate_uri

// Lua'da HTTP çağrısı sırasında akış kontrolü sağlar.
envoy.reloadable_features.lua_flow_control_while_http_call

// MMDB dosyalarının yeniden yüklenmesini etkinleştirir.
envoy.reloadable_features.mmdb_files_reload_enabled

// Uzantı adlarıyla arama yapmayı engeller.
envoy.reloadable_features.no_extension_lookup_by_name

// Zaman tabanlı hız sınırlayıcı token kovasını devre dışı bırakır.
envoy.reloadable_features.no_timer_based_rate_limit_token_bucket

// Orijinal hedefe idle timeout'a güvenerek davranır.
envoy.reloadable_features.original_dst_rely_on_idle_timeout

// MacOS üzerinde IPv6 DNS tercih eder.
envoy.reloadable_features.prefer_ipv6_dns_on_macos

// Proxy için 104 statüsünü etkinleştirir.
envoy.reloadable_features.proxy_104

// Proxy durumu haritalamaları için daha temel yanıt bayrakları ekler.
envoy.reloadable_features.proxy_status_mapping_more_core_response_flags

// QUIC istemci UDP soketleri için bağlantıyı etkinleştirir.
envoy.reloadable_features.quic_connect_client_udp_sockets

// QUIC ECN alımını etkinleştirir.
envoy.reloadable_features.quic_receive_ecn

// QUIC tüm istemcilere sunucu tercih edilen adresini gönderir.
envoy.reloadable_features.quic_send_server_preferred_address_to_all_clients

// QUIC sertifika sıkıştırmasını destekler.
envoy.reloadable_features.quic_support_certificate_compression

// QUIC upstream sabit sayıdaki paketleri okur.
envoy.reloadable_features.quic_upstream_reads_fixed_number_packets

// QUIC upstream soket adresi için okuma önbelleği kullanır.
envoy.reloadable_features.quic_upstream_socket_use_address_cache_for_read

// Geçersiz YAML verilerini reddeder.
envoy.reloadable_features.reject_invalid_yaml

// Akış sıfırlama hata kodunu raporlar.
envoy.reloadable_features.report_stream_reset_error_code

// HTTP/2 başlıklarını NGHTTP2 olmadan temizler.
envoy.reloadable_features.sanitize_http2_headers_without_nghttp2

// TE başlıklarını temizler.
envoy.reloadable_features.sanitize_te

// Yerel yanıt gönderir, tampon dolu olduğunda ve upstream isteği olduğunda.
envoy.reloadable_features.send_local_reply_when_no_buffer_and_upstream_request

// Proxy istekleri için DNS aramasını atlar.
envoy.reloadable_features.skip_dns_lookup_for_proxied_requests

// Katı süre doğrulaması yapar.
envoy.reloadable_features.strict_duration_validation

// TCP tünelleme, downstream FIN'i upstream treylerlerle gönderir.
envoy.reloadable_features.tcp_tunneling_send_downstream_fin_on_upstream_trailers

// UDP soketleri için birleştirilmiş okuma sınırını uygular.
envoy.reloadable_features.udp_socket_apply_aggregated_read_limit

// Yetersiz URL kodlamaya izin verir.
envoy.reloadable_features.uhv_allow_malformed_url_encoding

// Upstream uzak adresi bağlantı kullanır.
envoy.reloadable_features.upstream_remote_address_use_connection

// Mutlu gözyaşları için yapılandırma kullanımını etkinleştirir.
envoy.reloadable_features.use_config_in_happy_eyeballs

// Filtre yöneticisi durumunu downstream end_stream için kullanır.
envoy.reloadable_features.use_filter_manager_state_for_downstream_end_stream

// AWS kimlik bilgilerini almak için HTTP istemcisi kullanımını etkinleştirir.
envoy.reloadable_features.use_http_client_to_fetch_aws_credentials

// Otomatik SNI SAN için rota host mutasyonunu kullanır.
envoy.reloadable_features.use_route_host_mutation_for_auto_sni_san

// Proxy protokolü dinleyicisi için typed metadata kullanımını etkinleştirir.
envoy.reloadable_features.use_typed_metadata_in_proxy_protocol_listener

// CONNECT doğrulamasını yapar.
envoy.reloadable_features.validate_connect

// gRPC başlıklarını kaydetmeden önce doğrular.
envoy.reloadable_features.validate_grpc_header_before_log_grpc_status

// Upstream başlıklarını doğrular.
envoy.reloadable_features.validate_upstream_headers

// XDS yolunda çift nokta kodlamasından kaçınır.
envoy.reloadable_features.xdstp_path_avoid_colon_encoding

// İstemci soket oluşturma hatasına izin verir.
envoy.restart_features.allow_client_socket_creation_failure

// Worker thread'lerde slot yok edilmesine izin verir.
envoy.restart_features.allow_slot_destroy_on_worker_threads

// Dispatcher approximate now düzeltmesini etkinleştirir.
envoy.restart_features.fix_dispatcher_approximate_now

// QUIC sertifikaları paylaşılan TLS koduyla işler.
envoy.restart_features.quic_handle_certs_with_shared_tls_code

// EDS cache'ini ADS için kullanır.
envoy.restart_features.use_eds_cache_for_ads

// Hızlı protobuf hash kullanımını etkinleştirir.
envoy.restart_features.use_fast_protobuf_hash


// FALSE

// Yürütme bağlamı opsiyoneldir ve açıkça etkinleştirilmelidir.
// Daha fazla bilgi için: https://github.com/envoyproxy/envoy/issues/32012
envoy.restart_features.enable_execution_context

// Gözcü ve test flagi.
envoy.reloadable_features.test_feature_false

// Belirli bir süre test edildikten sonra varsayılan olarak etkinleştirilecektir.
envoy.reloadable_features.streaming_shadow

// Varsayılan olarak birleşik mux etkinleştirmek için true'ya ayarlayın.
envoy.reloadable_features.unified_mux

// Çalışma zamanının başlatılıp başlatılmadığını izlemek için kullanılır.
envoy.reloadable_features.runtime_initialized

// Envoy Mobile tarafından doğrulandıktan sonra true'ya çevrilecektir.
// Apple ve Android için varsayılan olursa birim test edilmelidir.
envoy.reloadable_features.always_use_v6

// Tüm TcpProxy::Filter::HttpStreamDecoderFilterCallbacks uygulandıktan sonra veya gereksiz olarak yorumlandıktan sonra true'ya çevrilecektir.
envoy.restart_features.upstream_http_filters_with_tcp_proxy

// QUICHE kendi etkinleştirme/devre dışı bırakma bayrağına sahip olduğunda eski hale gelecektir.
envoy.reloadable_features.quic_reject_all

// Evrensel Başlık Doğrulayıcı yeterince test edildiğinde true'ya çevrilecektir.
// Daha fazla bilgi için: https://github.com/envoyproxy/envoy/issues/10646
envoy.reloadable_features.enable_universal_header_validator

// QUIC ACK dinleyicisine günlük kaydını ertelemek için etkinleştirilecektir.
// Daha fazla bilgi için: https://github.com/envoyproxy/envoy/issues/29930
envoy.reloadable_features.quic_defer_logging_to_ack_listener

// GRO paket düşürme düzeltildiğinde kaldırılacaktır.
envoy.reloadable_features.prefer_quic_client_udp_gro

// Null adreslerin yeniden çözülmesini değerlendirir ve ya bir yapılandırma düğmesi yapar ya da kaldırır.
envoy.reloadable_features.reresolve_null_addresses

// Bağlantı yoksa yeniden çözümlemeyi değerlendirir ve ya bir yapılandırma düğmesi yapar ya da kaldırır.
envoy.reloadable_features.reresolve_if_no_connections

// Alpha modundan çıktıktan sonra true'ya çevrilecektir.
envoy.restart_features.xds_failover_support

// DNS önbelleğinde IP versiyonunu kaldırmak için ayarlanmış değerlendirir ve ya bir yapılandırma düğmesi yapar ya da kaldırır.
envoy.reloadable_features.dns_cache_set_ip_version_to_remove

// Ağ değişikliğinde kırık bağlantıları sıfırlamak için değerlendirir ve ya bir yapılandırma düğmesi yapar ya da kaldırır.
envoy.reloadable_features.reset_brokenness_on_nework_change

// google_grpc istemcisi için maksimum TLS sürümünü TLS1.2'ye ayarlamak için kullanılır, uygunluk kısıtlamaları gerektiğinde.
envoy.reloadable_features.google_grpc_disable_tls_13

// Üretim testi sonrasında true'ya çevrilecektir
// Bir akışın HTTP/2 veya HTTP/3 upstream yarı kapanması öncesinde açık kalıp kalmayacağını kontrol eder.
envoy.reloadable_features.allow_multiplexed_upstream_half_close