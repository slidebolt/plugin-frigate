package frigate

import (
	"fmt"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// MQTTConfig holds MQTT connection configuration
type MQTTConfig struct {
	Host        string
	Port        int
	User        string
	Password    string
	TopicPrefix string
}

// MQTTRuntime manages the MQTT connection
type MQTTRuntime struct {
	client mqtt.Client
	done   chan struct{}
}

// StartMQTT initializes and connects to the MQTT broker
func StartMQTT(cfg MQTTConfig, onMessage func(camera, key, payload string)) (*MQTTRuntime, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		return nil, nil
	}
	port := cfg.Port
	if port <= 0 {
		port = 1883
	}
	prefix := strings.Trim(strings.TrimSpace(cfg.TopicPrefix), "/")
	if prefix == "" {
		prefix = "frigate"
	}

	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://%s:%d", host, port))
	opts.SetClientID(fmt.Sprintf("slidebolt-plugin-frigate-%d", time.Now().UnixNano()))
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(2 * time.Second)
	if strings.TrimSpace(cfg.User) != "" {
		opts.SetUsername(cfg.User)
		opts.SetPassword(cfg.Password)
	}
	opts.SetDefaultPublishHandler(func(_ mqtt.Client, _ mqtt.Message) {})

	rt := &MQTTRuntime{done: make(chan struct{})}
	opts.OnConnect = func(c mqtt.Client) {
		topic := prefix + "/#"
		token := c.Subscribe(topic, 1, func(_ mqtt.Client, m mqtt.Message) {
			t := strings.Trim(m.Topic(), "/")
			parts := strings.Split(t, "/")
			if len(parts) < 3 {
				return
			}
			if parts[0] != prefix {
				return
			}
			camera := parts[1]
			key := strings.Join(parts[2:], "/")
			onMessage(camera, key, strings.TrimSpace(string(m.Payload())))
		})
		token.WaitTimeout(5 * time.Second)
	}

	client := mqtt.NewClient(opts)
	token := client.Connect()
	if !token.WaitTimeout(8*time.Second) || token.Error() != nil {
		if token.Error() != nil {
			return nil, token.Error()
		}
		return nil, fmt.Errorf("mqtt connect timeout")
	}

	rt.client = client
	return rt, nil
}

// Stop disconnects from the MQTT broker
func (m *MQTTRuntime) Stop() {
	if m == nil {
		return
	}
	select {
	case <-m.done:
	default:
		close(m.done)
	}
	if m.client != nil && m.client.IsConnected() {
		m.client.Disconnect(250)
	}
}
