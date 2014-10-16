package core

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/fsouza/go-dockerclient"
	. "github.com/mcuadros/dockership/logger"
)

var statusUp = regexp.MustCompile("^Up (.*)")
var imageIdRe = regexp.MustCompile("^(.*)/(.*):(.*)")

type Docker struct {
	enviroment *Enviroment
	client     *docker.Client
}

func NewDocker(enviroment *Enviroment) *Docker {
	Debug("Connected to docker", "enviroment", enviroment)
	c, _ := docker.NewClient(enviroment.DockerEndPoint)

	return &Docker{client: c, enviroment: enviroment}
}

func (d *Docker) Deploy(p *Project, commit Commit, dockerfile []byte, force bool) error {
	Info("Deploying dockerfile", "project", p, "commit", commit)
	if err := d.Clean(p, commit, force); err != nil {
		return err
	}

	if err := d.BuildImage(p, commit, dockerfile); err != nil {
		return err
	}

	return d.Run(p, commit)
}

func (d *Docker) Clean(p *Project, commit Commit, force bool) error {
	l, err := d.ListContainers(p)
	if err != nil {
		return err
	}

	if !force {
		for _, c := range l {
			if c.IsRunning() && c.Image.IsCommit(commit) {
				return errors.New("Current commit is already running")

			}
		}
	}

	Info("Cleaning all containers", "project", p)
	for _, c := range l {
		Info("Killing and removing image", "project", p, "container", c.GetShortId())
		err := d.killAndRemove(c)
		if err != nil {
			return err
		}
	}

	return nil
}

func (d *Docker) ListContainers(p *Project) ([]*Container, error) {
	Debug("Retrieving current containers", "project", p)

	l, err := d.client.ListContainers(docker.ListContainersOptions{
		All: true,
	})

	if err != nil {
		return nil, err
	}

	r := make([]*Container, 0)
	for _, c := range l {
		i := ImageId(c.Image)
		if i.BelongsTo(p) {
			r = append(r, &Container{
				Image:         i,
				APIContainers: c,
				Enviroment:    d.enviroment,
			})
		}
	}

	return r, nil
}

func (d *Docker) killAndRemove(c *Container) error {
	kopts := docker.KillContainerOptions{ID: c.ID}
	if err := d.client.KillContainer(kopts); err != nil {
		return err
	}

	ropts := docker.RemoveContainerOptions{ID: c.ID}
	if err := d.client.RemoveContainer(ropts); err != nil {
		return err
	}

	if err := d.client.RemoveImage(string(c.Image)); err != nil {
		return err
	}

	return nil
}

func (d *Docker) BuildImage(p *Project, commit Commit, dockerfile []byte) error {
	Info("Building image", "project", p, "commit", commit)

	inputbuf, outputbuf := bytes.NewBuffer(nil), bytes.NewBuffer(nil)
	outputbuf.WriteTo(os.Stdout)

	d.buildTar(dockerfile, inputbuf)

	image := d.getImageName(p, commit)
	opts := docker.BuildImageOptions{
		Name:           string(image),
		NoCache:        p.NoCache,
		RmTmpContainer: p.NoCache,
		InputStream:    inputbuf,
		OutputStream:   outputbuf,
	}

	return d.client.BuildImage(opts)
}

func (d *Docker) Run(p *Project, commit Commit) error {
	Debug("Creating container from image", "project", p, "commit", commit)
	c, err := d.createContainer(d.getImageName(p, commit))
	if err != nil {
		return err
	}

	Info("Running new container",
		"project", p,
		"commit", commit,
		"image", c.Image,
		"container", c.GetShortId(),
	)

	return d.startContainer(c)
}

func (d *Docker) getImageName(p *Project, commit Commit) ImageId {
	c := string(commit)
	if p.UseShortCommits {
		c = commit.GetShort()
	}

	return ImageId(fmt.Sprintf("%s/%s:%s", p.Owner, p.Repository, c))
}

func (d *Docker) createContainer(image ImageId) (*Container, error) {
	c, err := d.client.CreateContainer(docker.CreateContainerOptions{
		Config: &docker.Config{
			Image: string(image),
		},
	})

	if err != nil {
		return nil, err
	}

	return &Container{Image: image, APIContainers: docker.APIContainers{ID: c.ID}}, nil
}

func (d *Docker) startContainer(c *Container) error {
	return d.client.StartContainer(c.ID, &docker.HostConfig{
		PortBindings: map[docker.Port][]docker.PortBinding{
			"80/tcp": []docker.PortBinding{docker.PortBinding{
				HostIp:   "0.0.0.0",
				HostPort: "212",
			}},
		},
	})
}

func (d *Docker) buildTar(dockerfile []byte, buf *bytes.Buffer) *tar.Writer {
	t := time.Now()

	tr := tar.NewWriter(buf)
	tr.WriteHeader(&tar.Header{
		Name:       "Dockerfile",
		Size:       int64(len(dockerfile)),
		ModTime:    t,
		AccessTime: t,
		ChangeTime: t,
	})

	tr.Write(dockerfile)
	tr.Close()

	return tr
}

type ImageId string

func (i ImageId) BelongsTo(p *Project) bool {
	return strings.HasPrefix(string(i), fmt.Sprintf("%s/%s", p.Owner, p.Repository))
}

func (i ImageId) IsCommit(commit Commit) bool {
	s := strings.Split(string(i), ":")
	return strings.HasPrefix(s[1], commit.GetShort())
}

func (i ImageId) GetInfo() (owner, repository string, commit Commit) {
	m := imageIdRe.FindStringSubmatch(string(i))
	owner, repository = m[1], m[2]
	commit = Commit(m[3])

	return
}

type Container struct {
	Enviroment *Enviroment
	Image      ImageId
	docker.APIContainers
}

func (c *Container) IsRunning() bool {
	return statusUp.MatchString(c.Status)
}

func (c *Container) GetShortId() string {
	shortLen := 12
	if len(c.ID) < shortLen {
		shortLen = len(c.ID)
	}

	return c.ID[:shortLen]
}

func (c *Container) GetPorts() string {
	result := []string{}
	for _, port := range c.Ports {
		if port.IP == "" {
			result = append(result, fmt.Sprintf("%d/%s", port.PrivatePort, port.Type))
		} else {
			result = append(result, fmt.Sprintf("%s:%d->%d/%s", port.IP, port.PublicPort, port.PrivatePort, port.Type))
		}
	}
	return strings.Join(result, ", ")
}

type Enviroment struct {
	DockerEndPoint string
	Name           string
}

func (e *Enviroment) String() string {
	return e.Name
}
