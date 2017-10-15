package logger

type Highlighters struct {
	sync.RWMutex
	connection map[string]*regexp.Regexp
}

type LoggedMessage struct {
	Message      *lierc.IRCMessage
	ConnectionId string
	MessageId    int
	Highlight    bool
}

type Logger struct {
	highlighters *Highlighters
	loggable     map[string]string
	send         chan *LoggedMessage
	db           *sql.DB
	nsqConfig    *nsq.Config
	nsqHost      string
}

func CreateLogger(dsn string) {
	db, err := sql.Open("postgres", buildDSN())

	if err != nil {
		panic(err)
	}

	logger := &Logger{
		db:           db,
		loggable:     defaultLoggable(),
		highlighters: &Highlighters{},
		send:         make(chan *LoggedMessage),
		nsqConfig:    nsq.NewConfig(),
		nsqHost:      buildNSQHost(),
	}

	logger.updateHighlighters()
	logger.setupHighlightListener()
	logger.setupNSQConsumers()

	go func() {
		write, _ := nsq.NewProducer(l.nsqHost, l.nsqConfig)
		for {
			e := <-l.send
			json, _ := json.Marshal(e)
			write.Publish("logged", json)
			if e.Highlight || (e.Message.Direct && !e.Message.Prefix.Self) {
				write.Publish("highlight", json)
			}
		}
	}()

	return logger
}

func (l *Logger) setupNSQConsumers {
}

func buildNSQHost() string {
	return fmt.Sprintf("%s:4150", os.Getenv("NSQD_HOST"))
}

func buildDSN() string {
	user := os.Getenv("POSTGRES_USER")
	pass := os.Getenv("POSTGRES_PASSWORD")
	host := os.Getenv("POSTGRES_HOST")
	dbname := os.Getenv("POSTGRES_DB")
	nsqd := fmt.Sprintf("%s:4150", os.Getenv("NSQD_HOST"))

	return fmt.Sprintf("user=%s password=%s dbname=%s host=%s sslmode=disable", user, pass, dbname, host)
}

func (l *Logger) logType(m *lierc.IRCMessage) string {
	t, ok := l.loggable[m.Command]

	if ok {
		return t
	}

	if m.Command == "NOTICE" {
		if m.Direct {
			return "#"
		} else {
			return "status"
		}
	}

	if m.Command[0] == '4' || m.Command[0] == '5' || m.Command[0] == '9' {
		return "status"
	}

	return "pass"
}

func (l *Logger) updateHighlighters() {
	rows, err := l.db.Query("SELECT id,config->>'Highlight' FROM connection")
	defer rows.Close()

	if err != nil {
		panic(err)
	}

	l.highlighters.Lock()
	defer l.highlighters.Unlock()

	for rows.Next() {
		var id string
		var highlight string
		rows.Scan(&id, &highlight)

		fmt.Fprintf(os.Stderr, "Building regex for highlights '%s'\n", highlight)

		var terms []string

		err = json.Unmarshal([]byte(highlight), &terms)

		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to unmarshall highlights '%s'\n", err.Error)
			continue
		}

		var parts []string

		for _, term := range terms {
			matched, _ := regexp.MatchString("\\S", term)
			if matched {
				parts = append(parts, regexp.QuoteMeta(term))
			}
		}

		if len(parts) == 0 {
			continue
		}

		var source = "(?i)\\b(?:" + strings.Join(parts, "|") + ")\\b"
		fmt.Fprintf(os.Stderr, "Compiling regex '%s'\n", source)

		re, err := regexp.Compile(source)

		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to compile regexp '%s'\n", highlight)
			continue
		}

		l.highlighters.connection[id] = re
	}
}

func (l *Logger) insertPrivate(client_id string, nick string, time float64) error {
	_, err := l.db.Exec(
		"INSERT INTO private (connection, nick, time) VALUES($1,$2,to_timestamp($3)) ON CONFLICT (connection, nick) DO UPDATE SET time=to_timestamp($4)",
		client_id,
		nick,
		time,
		time,
	)
	return err
}

func (l *Logger) insertMessage(client_id string, m *lierc.IRCMessage, channel string, highlight bool) int {
	v, err := json.Marshal(m)

	if err != nil {
		panic(err)
	}

	var i int

	insert_err := l.db.QueryRow(
		"INSERT INTO log (connection, channel, command, message, time, self, highlight) VALUES($1,$2,$3,$4,to_timestamp($5),$6,$7) RETURNING id",
		client_id,
		strings.ToLower(channel),
		strings.ToUpper(m.Command),
		v,
		m.Time,
		m.Prefix.Self,
		highlight,
	).Scan(&i)

	if insert_err != nil {
		panic(insert_err)
	}

	return i
}

func (l *Logger) setupHighlightListener(dsn string) {
	reportProblem := func(ev pq.ListenerEventType, err error) {
		if err != nil {
			panic(err)
		}
	}

	listener := pq.NewListener(dsn, 10*time.Second, time.Minute, reportProblem)
	err := listener.Listen("highlights")

	if err != nil {
		panic(err)
	}

	go func() {
		for {
			fmt.Fprintf(os.Stderr, "Waiting for highlight change notify\n")
			<-listener.Notify
			fmt.Fprintf(os.Stderr, "Got highlight change notify\n")
			l.updateHighlighters()
		}
	}()
}
