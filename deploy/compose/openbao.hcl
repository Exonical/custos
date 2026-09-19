ui = false

storage "file" {
  path = "/openbao/data"
}

listener "tcp" {
  address         = "0.0.0.0:8200"
  tls_cert_file   = "/openbao/tls/openbao.crt"
  tls_key_file    = "/openbao/tls/openbao.key"
  tls_min_version = "tls13"
}

api_addr = "https://openbao:8200"
disable_mlock = true
