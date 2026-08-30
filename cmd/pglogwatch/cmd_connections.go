package main

import "github.com/cybertec-postgresql/pglogwatch"

func init() {
	commands["connections"] = command{
		summary: "connection counts by database, user, application and client",
		flags:   noFlags,
		run:     runConnections,
	}
}

// runConnections reports who is connecting.
//
// It counts connection EVENTS rather than sampling concurrent sessions: a log
// records arrivals and departures, not occupancy, so "connections by user" here
// means how many times each user connected. That distinction matters when
// reading the numbers -- a user with one long-lived pooled connection appears
// once, not continuously.
func runConnections(o *options) error {
	byDatabase := newCounter(o.top)
	byUser := newCounter(o.top)
	byApp := newCounter(o.top)
	byClient := newCounter(o.top)
	var connects, disconnects, failures int64

	err := o.eachRecord(func(r *pglogwatch.Record) error {
		switch classify(r) {
		case kindDisconnection:
			disconnects++
			return nil
		case kindConnection:
			// A failed authentication is a connection event too, and
			// counting it separately is the point of the report for
			// anyone chasing a misconfigured client.
			if r.Severity >= pglogwatch.SeverityError {
				failures++
			} else {
				connects++
			}
		default:
			return nil
		}

		addIfPresent(byDatabase, connectionDatabase(r))
		addIfPresent(byUser, connectionUser(r))
		addIfPresent(byApp, r.ApplicationName)
		addIfPresent(byClient, clientHost(r))
		return nil
	})
	if err != nil {
		return err
	}

	if o.jsonOut {
		return connectionsJSON(o, connects, disconnects, failures,
			byDatabase, byUser, byApp, byClient)
	}
	connectionsText(o, connects, disconnects, failures,
		byDatabase, byUser, byApp, byClient)
	return nil
}

func addIfPresent(c *counter, v []byte) {
	if len(v) > 0 {
		c.addBytes(v, 0, nil)
	}
}

// clientHost strips the port from a "host:port" connection_from value.
//
// Grouping by host and port would produce one group per connection, since the
// ephemeral port differs every time -- the same failure mode verbatim message
// grouping has.
func clientHost(r *pglogwatch.Record) []byte {
	v := r.ConnectionFrom
	for i := len(v) - 1; i >= 0; i-- {
		if v[i] == ':' {
			return v[:i]
		}
		if v[i] < '0' || v[i] > '9' {
			break // not a trailing port
		}
	}
	return v
}

func connectionsText(o *options, connects, disconnects, failures int64,
	byDatabase, byUser, byApp, byClient *counter,
) {
	t := newTable(o.stdout, "event", "count")
	t.add("connections", itoa(connects))
	t.add("failed connections", itoa(failures))
	t.add("disconnections", itoa(disconnects))
	t.flush()

	for _, sec := range []struct {
		title string
		c     *counter
	}{
		{"database", byDatabase},
		{"user", byUser},
		{"application", byApp},
		{"client", byClient},
	} {
		o.stdout.Write([]byte("\n")) //nolint:errcheck // report output
		tab := newTable(o.stdout, "count", sec.title)
		for _, g := range sec.c.top(o.top) {
			tab.add(itoa(g.count), g.key)
		}
		tab.flush()
	}
}

func connectionsJSON(o *options, connects, disconnects, failures int64,
	byDatabase, byUser, byApp, byClient *counter,
) error {
	j := newJSONWriter(o.stdout)
	j.begin()
	j.strS("report", "connections")
	j.numAlways("connections", connects)
	j.numAlways("failed_connections", failures)
	j.numAlways("disconnections", disconnects)
	j.end()

	for _, sec := range []struct {
		name string
		c    *counter
	}{
		{"database", byDatabase},
		{"user", byUser},
		{"application", byApp},
		{"client", byClient},
	} {
		for _, g := range sec.c.top(o.top) {
			j.begin()
			j.strS("report", "connections."+sec.name)
			j.numAlways("count", g.count)
			j.strS(sec.name, g.key)
			j.end()
		}
	}
	return j.flush()
}
