package itdog

type OPing struct {
	IP       string `json:"ip"`
	Result   any    `json:"result"`
	Name     string `json:"name"`
	Address  string `json:"address"`
	Province int    `json:"province"`
	Line     int    `json:"line"`
	Region   int    `json:"region"`
}

type OTcping struct {
	IP       string `json:"ip"`
	Result   any    `json:"result"`
	Name     string `json:"name"`
	Address  string `json:"address"`
	Province int    `json:"province"`
	Line     int    `json:"line"`
	Region   int    `json:"region"`
}

type OHttp struct {
	Type         string `json:"type"`
	Address      string `json:"address"`
	Name         string `json:"name"`
	IP           string `json:"ip"`
	HTTPCode     string `json:"http_code"`
	RedirectTime any    `json:"redirect_time"`
	Redirect     string `json:"redirect"`
	SSLTime      any    `json:"ssl_time"`
	DNSTime      any    `json:"dns_time"`
	ConnectTime  any    `json:"connect_time"`
	AllTime      any    `json:"all_time"`
	Head         string `json:"head"`
	Province     int    `json:"province"`
	Line         int    `json:"line"`
	Region       int    `json:"region"`
}

type ODNS struct {
	Type     string `json:"type"`
	Host     string `json:"host"`
	Result   any    `json:"result"`
	Time     any    `json:"time"`
	Line     int    `json:"line"`
	Name     string `json:"name"`
	Region   int    `json:"region"`
	Province int    `json:"province"`
}

type StatEntry struct {
	Line     int
	Region   int
	Province int
	Name     string
	Delay    float64
}

type delayName struct {
	Name  string  `json:"name"`
	Delay float64 `json:"delay"`
}

type statBucket struct {
	Nodes int       `json:"nodes"`
	All   float64   `json:"all"`
	Argv  string    `json:"argv,omitempty"`
	Fast  delayName `json:"fast"`
	Slow  delayName `json:"slow"`
}
