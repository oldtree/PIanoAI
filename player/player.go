package player

import (
	"fmt"
	"math"
	"os"
	"os/signal"
	"time"

	"github.com/schollz/rpiai-piano/ai"
	"github.com/schollz/rpiai-piano/music"
	"github.com/schollz/rpiai-piano/piano"
	log "github.com/sirupsen/logrus"
)

func init() {
	// Log as JSON instead of the default ASCII formatter.
	// log.SetFormatter(&log.JSONFormatter{})

	// Output to stdout instead of the default stderr
	// Can be any io.Writer, see below for File example
	// log.SetOutput(os.Stdout)

	// Only log the warning severity or above.
	log.SetLevel(log.DebugLevel)
}

// Player is the main structure which facilitates the Piano, and the AI.
// The Player spawns threads for listening to events on the Piano, and also
// spawns threads for playing notes on the piano. It also spawns threads
// for doing the machine learning and using the results.
type Player struct {
	// BPM is the beats per minute
	BPM int
	// Beat counts the number of 1/64 beats
	Beat int
	// Key stores the key of the song (TODO: Add in key-signature constraints)
	Key string

	// Piano is the piano that does the playing, the MIDI keyboard
	Piano *piano.Piano
	// MusicFuture is a map of future chords to play
	MusicFuture *music.Music
	// MusicHistory is a map of all the previous notes played
	MusicHistory     *music.Music
	MusicHistoryFile string

	// AI stores the AI being used
	AI *ai.AI
	// BeatsOfSilence waits this number of beats before asking
	// the AI for an improvisation
	BeatsOfSilence int
	// LastNote is the beat of the last note played
	LastNote int
	// HighPassFilter only uses notes above a certain level
	// for computing last note
	HighPassFilter int
	// KeysCurrentlyPressed keeps track of whether a key is down (should be 0 if no keys are down)
	KeysCurrentlyPressed int
}

// Init initializes the parameters and connects up the piano
func New(bpm int, beats ...int) (p *Player, err error) {
	p = new(Player)
	logger := log.WithFields(log.Fields{
		"function": "Player.Init",
	})
	p.BPM = bpm
	p.Beat = 0
	p.Key = "C"

	logger.Debug("Loading piano")
	p.Piano, err = piano.New()
	if err != nil {
		return
	}

	logger.Debug("Loading music")
	p.MusicFuture = music.New()
	var errOpening error
	p.MusicHistoryFile = "music_history.json"
	p.MusicHistory, errOpening = music.Open(p.MusicHistoryFile)
	if errOpening != nil {
		logger.Warn(errOpening.Error())
		p.MusicHistory = music.New()
	} else {
		logger.Info("Loaded previous music history")
	}

	if len(beats) == 1 {
		p.BeatsOfSilence = beats[0]
	} else {
		p.BeatsOfSilence = 4 * 64
	}
	p.LastNote = 0
	p.HighPassFilter = 70

	logger.Debug("Loading AI")
	p.AI = ai.New()
	p.AI.HighPassFilter = p.HighPassFilter

	return
}

// Close will do the shutdown routines before exiting
func (p *Player) Close() (err error) {
	logger := log.WithFields(log.Fields{
		"function": "Player.Close",
	})
	logger.Debug("Closing piano...")
	err = p.Piano.Close()
	if err != nil {
		logger.Error(err.Error())
	}
	return
}

// Start initializes the metronome which keeps track of beats
// Each beat will start new threads to Emit new chords, and/or
// generate new Improvisation
func (p *Player) Start() {
	logger := log.WithFields(log.Fields{
		"function": "Player.Start",
	})

	// Exit on Ctl+C
	doneChan := make(chan bool)
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for sig := range c {
			logger.Debugf("%+v", sig)
			// sig is a ^C, handle it
			p.Close()
			doneChan <- true
		}
	}()

	// start listening
	go p.Listen()

	p.Beat = 0
	tickChan := time.NewTicker(time.Millisecond * time.Duration((1000*60/p.BPM)/64)).C
	logger.Infof("BPM:  %d, tick size: %2.1f ms", p.BPM, time.Duration(time.Millisecond*time.Duration((1000*60/float64(p.BPM))/64)).Seconds()*1000)
	for {
		select {
		case <-tickChan:
			// if p.Beat == math.Trunc(p.Beat) {
			// 	logger.Debugf("beat %2.0f", p.Beat)
			// }
			p.Beat += 1
			go p.Emit(p.Beat)

			// if p.Beat-p.LastNote > p.BeatsOfSilence && p.KeysCurrentlyPressed == 0 {
			// 	go p.Improvisation()
			// }

			if math.Mod(float64(p.Beat), 64) == 0 {
				logger.WithFields(log.Fields{
					"Beat":     p.Beat,
					"LastNote": p.LastNote,
					"KeysDown": p.KeysCurrentlyPressed,
				}).Debug("metronome")
			}

		case <-doneChan:
			fmt.Println("Done")
			return
		}
	}
}

func (p *Player) Teach() (err error) {
	logger := log.WithFields(log.Fields{
		"function": "Player.Teach",
	})
	knownNotes := p.MusicHistory.GetAll()
	p.LastNote = p.Beat + 64*4 // give some time to start
	logger.Info("Sending history to AI")
	err = p.AI.Learn2(knownNotes)
	if err != nil {
		logger.Warn(err.Error())
		return
	}
	return
}

// Improvisation generates an improvisation from the AI
// and loads into the next beats to be playing
func (p *Player) Improvisation() {
	logger := log.WithFields(log.Fields{
		"function": "Player.Improvisation",
	})
	if !p.AI.HasLearned {
		err := p.Teach()
		if err != nil {
			return
		}
	}
	notes, err := p.AI.Lick2(p.Beat)
	if err != nil {
		logger.Error(err.Error())
	}
	newNotes := notes.GetAll()
	for _, note := range newNotes {
		p.MusicFuture.AddNote(note)
	}
	logger.Infof("Added %d notes from AI", len(newNotes))
}

// Emit will play/stop notes depending on the current beat.
// This should be run in a separate thread.
func (p *Player) Emit(beat int) {
	hasNotes, notes := p.MusicFuture.Get(beat)
	if hasNotes {
		go p.Piano.PlayNotes(notes, p.BPM)
		p.LastNote = p.Beat
	}
}

// Listen tells the player to listen to events from the
// piano MIDI connection. This is meant to be run in a
// separate thread.
func (p *Player) Listen() {
	logger := log.WithFields(log.Fields{
		"function": "Player.Listen",
	})

	ch := p.Piano.InputStream.Listen()
	for {
		event := <-ch
		note := music.Note{
			On:       event.Data2 > 0,
			Pitch:    int(event.Data1),
			Velocity: int(event.Data2),
			Beat:     p.Beat,
		}

		if note.Pitch == 21 {
			if !note.On {
				continue
			}
			p.MusicHistory.Save(p.MusicHistoryFile)
			logger.Info("Saved music_history.json")
		} else if note.Pitch == 22 {
			if !note.On {
				continue
			}
			logger.Info("Playing back history")
			for _, note := range p.MusicHistory.GetAll() {
				logger.Infof("Adding %+v to future", note)
				p.MusicFuture.AddNote(note)
			}
			p.Beat = 0
		} else if note.Pitch == 107 {
			if !note.On {
				continue
			}
			p.Teach()
		} else if note.Pitch == 108 {
			if !note.On {
				continue
			}
			p.Improvisation()
		} else {
			if !note.On && note.Pitch > p.HighPassFilter {
				p.LastNote = p.Beat
				p.KeysCurrentlyPressed--
			}
			if note.On && note.Pitch > p.HighPassFilter {
				p.KeysCurrentlyPressed++
			}
			logger.Infof("Adding %+v", note)
			go p.MusicHistory.AddNote(note)
		}
	}
}
