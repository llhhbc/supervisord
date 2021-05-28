package process

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ochinchina/supervisord/config"
	log "github.com/sirupsen/logrus"
)

// Manager manage all the process in the supervisor
type Manager struct {
	procs          map[string]*Process
	eventListeners map[string]*Process
	lock           sync.Mutex
	procCacheFile  string
}

// NewManager create a new Manager object
func NewManager(procFile string) *Manager {
	res := &Manager{procs: make(map[string]*Process),
		eventListeners: make(map[string]*Process),
	}
	if procFile != "" {
		res.procCacheFile = procFile
		// load info from cache
		res.LoadProcesses()
	}
	go res.FlushCache()
	return res
}

// CreateProcess create a process (program or event listener) and add to this manager
func (pm *Manager) CreateProcess(supervisorID string, config *config.Entry) *Process {
	pm.lock.Lock()
	defer pm.lock.Unlock()
	//defer pm.FlushCache()
	if config.IsProgram() {
		return pm.createProgram(supervisorID, config)
	} else if config.IsEventListener() {
		return pm.createEventListener(supervisorID, config)
	} else {
		return nil
	}
}

// StartAutoStartPrograms start all the program if its autostart is true
func (pm *Manager) StartAutoStartPrograms() {
	pm.ForEachProcess(func(proc *Process) {
		if proc.isAutoStart() {
			proc.Start(false)
		}
	})
}

func (pm *Manager) createProgram(supervisorID string, config *config.Entry) *Process {
	procName := config.GetProgramName()

	proc, ok := pm.procs[procName]

	if !ok {
		proc = NewProcess(supervisorID, config)
		pm.procs[procName] = proc
	}
	log.Info("create process:", procName)
	return proc
}

func (pm *Manager) createEventListener(supervisorID string, config *config.Entry) *Process {
	eventListenerName := config.GetEventListenerName()

	evtListener, ok := pm.eventListeners[eventListenerName]

	if !ok {
		evtListener = NewProcess(supervisorID, config)
		pm.eventListeners[eventListenerName] = evtListener
	}
	log.Info("create event listener:", eventListenerName)
	return evtListener
}

// Add add the process to this process manager
func (pm *Manager) Add(name string, proc *Process) {
	pm.lock.Lock()
	defer pm.lock.Unlock()
	pm.procs[name] = proc
	log.Info("add process:", name)
}

// Remove remove the process from the manager
//
// Arguments:
// name - the name of program
//
// Return the process or nil
func (pm *Manager) Remove(name string) *Process {
	pm.lock.Lock()
	defer pm.lock.Unlock()
	//defer pm.FlushCache()
	proc, _ := pm.procs[name]
	delete(pm.procs, name)
	log.Info("remove process:", name)
	return proc
}

// Find find process by program name return process if found or nil if not found
func (pm *Manager) Find(name string) *Process {
	procs := pm.FindMatch(name)
	if len(procs) == 1 {
		if procs[0].GetName() == name || name == fmt.Sprintf("%s:%s", procs[0].GetGroup(), procs[0].GetName()) {
			return procs[0]
		}
	}
	return nil
}

// FindMatch find the program with one of following format:
// - group:program
// - group:*
// - program
func (pm *Manager) FindMatch(name string) []*Process {
	result := make([]*Process, 0)
	if pos := strings.Index(name, ":"); pos != -1 {
		groupName := name[0:pos]
		programName := name[pos+1:]
		pm.ForEachProcess(func(p *Process) {
			if p.GetGroup() == groupName {
				if programName == "*" || programName == p.GetName() {
					result = append(result, p)
				}
			}
		})
	} else {
		pm.lock.Lock()
		defer pm.lock.Unlock()
		proc, ok := pm.procs[name]
		if ok {
			result = append(result, proc)
		}
	}
	if len(result) <= 0 {
		log.Info("fail to find process:", name)
	}
	return result
}

// Clear clear all the processes
func (pm *Manager) Clear() {
	pm.lock.Lock()
	defer pm.lock.Unlock()
	pm.procs = make(map[string]*Process)
}

// ForEachProcess process each process in sync mode
func (pm *Manager) ForEachProcess(procFunc func(p *Process)) {
	pm.lock.Lock()
	defer pm.lock.Unlock()
	//defer pm.FlushCache()

	procs := pm.getAllProcess()
	for _, proc := range procs {
		procFunc(proc)
	}
}

// AsyncForEachProcess handle each process in async mode
// Args:
// - procFunc, the function to handle the process
// - done, signal the process is completed
// Returns: number of total processes
func (pm *Manager) AsyncForEachProcess(procFunc func(p *Process), done chan *Process) int {
	pm.lock.Lock()
	defer pm.lock.Unlock()
	//defer pm.FlushCache()

	procs := pm.getAllProcess()

	for _, proc := range procs {
		go forOneProcess(proc, procFunc, done)
	}
	return len(procs)
}

func forOneProcess(proc *Process, action func(p *Process), done chan *Process) {
	action(proc)
	done <- proc
}

func (pm *Manager) getAllProcess() []*Process {
	tmpProcs := make([]*Process, 0)
	for _, proc := range pm.procs {
		tmpProcs = append(tmpProcs, proc)
	}
	return sortProcess(tmpProcs)
}

// StopAllProcesses stop all the processes managed by this manager
func (pm *Manager) StopAllProcesses() {
	var wg sync.WaitGroup

	pm.ForEachProcess(func(proc *Process) {
		wg.Add(1)

		go func(wg *sync.WaitGroup) {
			defer wg.Done()

			proc.Stop(true)
		}(&wg)
	})

	wg.Wait()
	//pm.FlushCache()
}

func sortProcess(procs []*Process) []*Process {
	progConfigs := make([]*config.Entry, 0)
	for _, proc := range procs {
		if proc.config.IsProgram() {
			progConfigs = append(progConfigs, proc.config)
		}
	}

	result := make([]*Process, 0)
	p := config.NewProcessSorter()
	for _, config := range p.SortProgram(progConfigs) {
		for _, proc := range procs {
			if proc.config == config {
				result = append(result, proc)
			}
		}
	}

	return result
}

type CacheInfo struct {
	Procs          map[string]*Process
	EventListeners map[string]*Process
}

func (pm *Manager) LoadProcesses()  {
	cache := &CacheInfo{}
	f, err := os.OpenFile(pm.procCacheFile, os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		log.Warningf("open cache file %s failed %v, proc cache is skipped. ", pm.procCacheFile, err)
		return
	}
	defer f.Close()
	body, err := ioutil.ReadAll(f)
	if err != nil && err != io.EOF {
		log.Errorf("read cache file failed %v. ", err)
		return
	}
	if body == nil || len(body) == 0 {
		return
	}
	//start, end := 0,len(body)
	//for i:=0;i<len(body);i++ {
	//	if start == 0 && body[i] != 0x00 {
	//		start = i
	//	}
	//	if i>start && body[i] == 0x00 {
	//		end = i
	//		break
	//	}
	//}
	//body = body[start:end]
	err = json.Unmarshal(body, cache)
	if err != nil {
		log.Errorf("unmarshal cache %s file %s failed %v. ", string(body), pm.procCacheFile, err)
		return
	}
	pm.procs = cache.Procs
	pm.eventListeners = cache.EventListeners
}

func (pm *Manager) FlushCache()  {
	log.Info("do flush cache. ")
	if pm.procCacheFile == "" {
		return
	}

	f, err := os.OpenFile(pm.procCacheFile, os.O_RDWR|os.O_TRUNC, 0666)
	if err != nil {
		log.Warningf("open cache file %s failed %v, proc cache is skipped. ", pm.procCacheFile, err)
		return
	}

	t := time.NewTicker(time.Second*10)
	for {
		select{
		case <- t.C:
			log.Infoln("begin to sync cache. ")
			pm.lock.Lock()
			c := CacheInfo{}
			c.EventListeners = pm.eventListeners
			c.Procs = pm.procs
			m, _ := json.Marshal(&c)
			f.Truncate(0)
			f.Seek(0, 0)
			f.Write(m)
			f.Sync()
			log.Infoln("sync cache file ok. ")
			pm.lock.Unlock()
		}
	}
	f.Close()
}