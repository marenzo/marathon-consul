package consul

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/allegro/marathon-consul/apps"
	"github.com/allegro/marathon-consul/metrics"
	consulapi "github.com/hashicorp/consul/api"
	"net/url"
	"strconv"
)

type ConsulServices interface {
	GetAllServices() ([]*consulapi.CatalogService, error)
	GetServices(name string) ([]*consulapi.CatalogService, error)
	Register(task *apps.Task, app *apps.App) error
	Deregister(serviceId apps.TaskId, agentAddress string) error
	GetAgent(agentAddress string) (*consulapi.Client, error)
}

type Consul struct {
	agents Agents
	config ConsulConfig
}

func New(config ConsulConfig) *Consul {
	return &Consul{
		agents: NewAgents(&config),
		config: config,
	}
}

func (c *Consul) GetServices(name string) ([]*consulapi.CatalogService, error) {
	agent, err := c.agents.GetAnyAgent()
	if err != nil {
		return nil, err
	}
	datacenters, err := agent.Catalog().Datacenters()
	if err != nil {
		return nil, err
	}
	var allServices []*consulapi.CatalogService

	for _, dc := range datacenters {
		dcAwareQuery := &consulapi.QueryOptions{
			Datacenter: dc,
		}
		services, _, err := agent.Catalog().Service(name, c.config.Tag, dcAwareQuery)
		if err != nil {
			return nil, err
		}
		allServices = append(allServices, services...)
	}
	return allServices, nil
}

func (c *Consul) GetAllServices() ([]*consulapi.CatalogService, error) {
	agent, err := c.agents.GetAnyAgent()
	if err != nil {
		return nil, err
	}
	datacenters, err := agent.Catalog().Datacenters()
	if err != nil {
		return nil, err
	}
	var allInstances []*consulapi.CatalogService

	for _, dc := range datacenters {
		dcAwareQuery := &consulapi.QueryOptions{
			Datacenter: dc,
		}
		services, _, err := agent.Catalog().Services(dcAwareQuery)
		if err != nil {
			return nil, err
		}
		for service, tags := range services {
			if contains(tags, c.config.Tag) {
				serviceInstances, _, err := agent.Catalog().Service(service, c.config.Tag, dcAwareQuery)
				if err != nil {
					return nil, err
				}
				allInstances = append(allInstances, serviceInstances...)
			}
		}
	}
	return allInstances, nil
}

func contains(slice []string, search string) bool {
	for _, element := range slice {
		if element == search {
			return true
		}
	}
	return false
}

func (c *Consul) Register(task *apps.Task, app *apps.App) error {
	var err error
	service := c.marathonTaskToConsulService(task, app)
	if value, ok := app.Labels["consul"]; ok && value == "true" {
		log.WithField("Id", app.ID).Warn("Warning! Application configuration is deprecated (labeled as `consul:true`). Support for special `true` value will be removed in the future!")
	}
	metrics.Time("consul.register", func() { err = c.register(service) })
	if err != nil {
		metrics.Mark("consul.register.error")
	} else {
		metrics.Mark("consul.register.success")
	}
	return err
}

func (c *Consul) register(service *consulapi.AgentServiceRegistration) error {
	agent, err := c.agents.GetAgent(service.Address)
	if err != nil {
		return err
	}
	fields := log.Fields{
		"Name":    service.Name,
		"Id":      service.ID,
		"Tags":    service.Tags,
		"Address": service.Address,
		"Port":    service.Port,
	}
	log.WithFields(fields).Info("Registering")

	err = agent.Agent().ServiceRegister(service)
	if err != nil {
		log.WithError(err).WithFields(fields).Error("Unable to register")
	}
	return err
}

func (c *Consul) Deregister(serviceId apps.TaskId, agentAddress string) error {
	var err error
	metrics.Time("consul.deregister", func() { err = c.deregister(serviceId, agentAddress) })
	if err != nil {
		metrics.Mark("consul.deregister.error")
	} else {
		metrics.Mark("consul.deregister.success")
	}
	return err
}

func (c *Consul) deregister(serviceId apps.TaskId, agentAddress string) error {
	agent, err := c.agents.GetAgent(agentAddress)
	if err != nil {
		return err
	}

	log.WithField("Id", serviceId).WithField("Address", agentAddress).Info("Deregistering")

	err = agent.Agent().ServiceDeregister(serviceId.String())
	if err != nil {
		log.WithError(err).WithField("Id", serviceId).WithField("Address", agentAddress).Error("Unable to deregister")
	}
	return err
}

func (c *Consul) GetAgent(agentAddress string) (*consulapi.Client, error) {
	return c.agents.GetAgent(agentAddress)
}

func (c *Consul) marathonTaskToConsulService(task *apps.Task, app *apps.App) *consulapi.AgentServiceRegistration {
	return &consulapi.AgentServiceRegistration{
		ID:      task.ID.String(),
		Name:    app.ConsulServiceName(),
		Port:    task.Ports[0],
		Address: task.Host,
		Tags:    c.marathonLabelsToConsulTags(app.Labels),
		Check:   c.marathonToConsulCheck(task, app.HealthChecks),
	}
}

func (c *Consul) marathonToConsulCheck(task *apps.Task, healthChecks []apps.HealthCheck) *consulapi.AgentServiceCheck {
	//	TODO: Handle all types of checks
	for _, check := range healthChecks {
		if check.Protocol == "HTTP" {
			return &consulapi.AgentServiceCheck{
				HTTP: (&url.URL{
					Scheme: "http",
					Host:   task.Host + ":" + strconv.Itoa(task.Ports[check.PortIndex]),
					Path:   check.Path,
				}).String(),
				Interval: fmt.Sprintf("%ds", check.IntervalSeconds),
				Timeout:  fmt.Sprintf("%ds", check.TimeoutSeconds),
			}
		}
	}
	return nil
}

func (c *Consul) marathonLabelsToConsulTags(labels map[string]string) []string {
	tags := []string{c.config.Tag}
	for key, value := range labels {
		if value == "tag" {
			tags = append(tags, key)
		}
	}
	return tags
}
