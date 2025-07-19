package core

import (
	"fmt"
)

type Client struct {
	ClientId   string
	TargetNode *Node
}

func NewClient(clientId string) *Client {
	return &Client{
		ClientId:   clientId,
		TargetNode: nil,
	}
}

func (c *Client) Connect(node *Node) error {
	if c.TargetNode != nil {
		return fmt.Errorf("client %s is already connected to Node %s", c.ClientId, c.TargetNode.ID)
	}
	c.TargetNode = node
	node.AddClient(c.ClientId)
	return nil
}

func (c *Client) Disconnect() {
	if c.TargetNode != nil {
		c.TargetNode.RemoveClient(c.ClientId)
		c.TargetNode = nil
	}
}

func (c *Client) Subscribe(topic string, cb MESSAGE_CALLBACK) error {
	if c.TargetNode == nil {
		return fmt.Errorf("client %s not connected to any Node", c.ClientId)
	}
	c.TargetNode.addLocalSub(topic, c.ClientId, cb)
	return nil
}

func (c *Client) Unsubscribe(topic string) error {
	if c.TargetNode == nil {
		return fmt.Errorf("client %s not connected to any Node", c.ClientId)
	}
	c.TargetNode.removeLocalSub(topic, c.ClientId)
	return nil
}

func (c *Client) Publish(topic string, message string) error {
	if c.TargetNode == nil {
		return fmt.Errorf("client %s not connected to any Node", c.ClientId)
	}
	c.TargetNode.publishOnTopic(topic, message)
	return nil
}
