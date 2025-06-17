package main

import "fmt"

const (
	defaultMaxFrameSize         = 4096
	defaultMaxInactiveSeconds   = 10
	defaultMaxSendQueueSize     = 1000
	defaultMaxConnectionsPerSec = 5
	defaultToken                = "default_token"
)

type Client struct {
	clientId   string
	targetNode *Node
}

func NewClient(clientId string) *Client {
	return &Client{
		clientId:   clientId,
		targetNode: nil,
	}
}

func (c *Client) Connect(node *Node) error {
	if c.targetNode != nil {
		return fmt.Errorf("client %s is already connected to node %s", c.clientId, c.targetNode.ID)
	}
	c.targetNode = node
	node.AddClient(c.clientId)
	return nil
}

func (c *Client) Disconnect() {
	if c.targetNode != nil {
		c.targetNode.RemoveClient(c.clientId)
		c.targetNode = nil
	}
}

func (c *Client) Subscribe(topic string, cb MESSAGE_CALLBACK) error {
	if c.targetNode == nil {
		return fmt.Errorf("client %s not connected to any node", c.clientId)
	}
	c.targetNode.addLocalSub(topic, c.clientId, cb)
	return nil
}

func (c *Client) Unsubscribe(topic string) error {
	if c.targetNode == nil {
		return fmt.Errorf("client %s not connected to any node", c.clientId)
	}
	c.targetNode.removeLocalSub(topic, c.clientId)
	return nil
}

func (c *Client) Publish(topic string, message string) error {
	if c.targetNode == nil {
		return fmt.Errorf("client %s not connected to any node", c.clientId)
	}
	c.targetNode.publishOnTopic(topic, message)
	return nil
}
