package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// --- CONFIGURACIÓN YAML ---
type Config struct {
	App struct {
		SigExtPath     string `yaml:"sigext_path"`
		SigExtFlags    string `yaml:"sigext_flags"`
		Classification struct {
			// Cambiamos el nombre en el struct para reflejar que son REGEX
			AnalogRegex  []string `yaml:"analog_output_regex"`
			DigitalRegex []string `yaml:"digital_output_regex"`
		} `yaml:"classification"`
		Spares struct {
			DO string `yaml:"do"`
			DI string `yaml:"di"`
			AO string `yaml:"ao"`
			AI string `yaml:"ai"`
		} `yaml:"spares"`
	} `yaml:"app"`
}

const (
	RelativePathToResource = `C\CWave_Micro\R\RTU_RESOURCE`
	ConfigFile             = "config.yaml"
	VarDefFile             = "__vardef.ini"
	ListFile               = "__lists.ini"
)

var (
	GlobalConfig                   Config
	ListAO, ListAI, ListDO, ListDI []string
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	fmt.Println("--- Generador DNP3 CLI v3.2 (Regex Logic) ---")

	projectPathPtr := flag.String("path", "", "Ruta raíz del proyecto")
	nodeNamePtr := flag.String("node", "", "Nombre del Nodo")
	skipExtPtr := flag.Bool("skip-ext", false, "Saltar ejecución de SIGEXT")

	flag.Parse()

	if *projectPathPtr == "" || *nodeNamePtr == "" {
		// Fallback para desarrollo (Opcional)
		if *projectPathPtr == "" {
			log.Fatal("Uso: dnpgen.exe -path \"C:\\Ruta\" -node \"NombreNodo\"")
		}
	}

	absProjectPath, err := filepath.Abs(*projectPathPtr)
	if err != nil {
		log.Fatalf("Error ruta absoluta: %v", err)
	}

	loadConfiguration()

	resourceDir := filepath.Join(absProjectPath, RelativePathToResource)
	sigFile := filepath.Join(resourceDir, *nodeNamePtr+".SIG")
	mwtFile := filepath.Join(absProjectPath, *nodeNamePtr+".mwt")

	if _, err := os.Stat(mwtFile); os.IsNotExist(err) {
		mwtFile = filepath.Join(resourceDir, *nodeNamePtr+".mwt")
	}

	if _, err := os.Stat(resourceDir); os.IsNotExist(err) {
		log.Fatalf("[FATAL] Recurso no encontrado: %s", resourceDir)
	}

	if err := os.Chdir(resourceDir); err != nil {
		log.Fatalf("Error accediendo a directorio: %v", err)
	}

	if !*skipExtPtr {
		log.Println("Ejecutando SIGEXT...")
		err := runSigExt(GlobalConfig.App.SigExtPath, GlobalConfig.App.SigExtFlags, mwtFile, *nodeNamePtr, sigFile)
		if err != nil {
			log.Printf("[ERROR] SIGEXT: %v", err)
		}
	}

	if _, err := os.Stat(sigFile); os.IsNotExist(err) {
		log.Fatalf("[FATAL] No existe .SIG: %s", sigFile)
	}

	log.Printf("Procesando: %s", filepath.Base(sigFile))
	if err := processSigFile(sigFile); err != nil {
		log.Fatalf("[FATAL] Error procesando: %v", err)
	}

	log.Println("Generando __lists.ini...")
	if err := generateListsFile(); err != nil {
		log.Fatalf("[FATAL] Error escribiendo INI: %v", err)
	}

	fmt.Println("\n--- RESUMEN ---")
	fmt.Printf("DI: %d | DO: %d | AI: %d | AO: %d\n", len(ListDI), len(ListDO), len(ListAI), len(ListAO))

	time.Sleep(1 * time.Second)
}

func loadConfiguration() {
	exePath, _ := os.Executable()
	configPathExe := filepath.Join(filepath.Dir(exePath), ConfigFile)
	configPathCWD := ConfigFile

	var f *os.File
	var err error

	if _, errStat := os.Stat(configPathExe); errStat == nil {
		f, err = os.Open(configPathExe)
	} else if _, errStat := os.Stat(configPathCWD); errStat == nil {
		f, err = os.Open(configPathCWD)
	} else {
		log.Fatalf("No se encuentra %s", ConfigFile)
	}

	if err != nil {
		log.Fatalf("Error abriendo config: %v", err)
	}
	defer f.Close()

	if err := yaml.NewDecoder(f).Decode(&GlobalConfig); err != nil {
		log.Fatalf("YAML malformado: %v", err)
	}
}

func runSigExt(exePath, flags, mwtPath, nodeName, sigPath string) error {
	if _, err := os.Stat(exePath); os.IsNotExist(err) {
		return fmt.Errorf("exe no encontrado")
	}
	args := []string{}
	if flags != "" {
		args = append(args, strings.Fields(flags)...)
	}
	args = append(args, mwtPath, nodeName, sigPath)
	return exec.Command(exePath, args...).Run()
}

// --- NUEVA LÓGICA DE REGEX ---
// isMatchRegex verifica si el nombre cumple con alguna de las expresiones regulares del YAML
func isMatchRegex(name string, patterns []string) bool {
	for _, p := range patterns {
		// regexp.MatchString compila y verifica.
		// Si el patrón es inválido, devolverá error (aquí lo ignoramos y asume false)
		matched, _ := regexp.MatchString(p, name)
		if matched {
			return true
		}
	}
	return false
}

func processSigFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	ListAO, ListAI, ListDO, ListDI = []string{}, []string{}, []string{}, []string{}
	spares := GlobalConfig.App.Spares
	rules := GlobalConfig.App.Classification

	scanner := bufio.NewScanner(file)
	re := regexp.MustCompile(`SIG=@GV\.([\w\d_]+)\s+TYPE=([A-Z]+)`)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "SIG=") {
			continue
		}

		matches := re.FindStringSubmatch(line)
		if matches != nil {
			varName := matches[1]
			varType := matches[2]
			fullName := "@GV." + varName

			// Nota: La lógica de _SPAN ya está manejada por el regex _SP($|_) en el YAML

			// --- LÓGICA ESPEJO ---

			// 1. ANALÓGICAS
			if strings.Contains(varType, "AA") || strings.Contains(varType, "REAL") {

				// AHORA USAMOS REGEX
				// Esto validará "LIT.*_H_H" -> Solo si tiene LIT y H_H es true.
				// FT041_H_H -> Fallará el regex, por tanto isOutput = false (ENTRADA) -> Correcto.
				isOutput := isMatchRegex(varName, rules.AnalogRegex)

				if isOutput {
					ListAO = append(ListAO, fullName)
					// Spare en AI con nombre para depurar
					ListAI = append(ListAI, fmt.Sprintf("%s(%s)", spares.AI, varName))
				} else {
					ListAI = append(ListAI, fullName)
					// Spare en AO con nombre para depurar
					ListAO = append(ListAO, fmt.Sprintf("%s(%s)", spares.AO, varName))
				}

				// 2. DIGITALES
			} else if strings.Contains(varType, "LA") || strings.Contains(varType, "BOOL") {

				isOutput := isMatchRegex(varName, rules.DigitalRegex)

				if isOutput {
					ListDO = append(ListDO, fullName)
					ListDI = append(ListDI, fmt.Sprintf("%s(%s)", spares.DI, varName))
				} else {
					ListDI = append(ListDI, fullName)
					ListDO = append(ListDO, fmt.Sprintf("%s(%s)", spares.DO, varName))
				}

			} else if varType == "AO" {
				ListAO = append(ListAO, fullName)
				ListAI = append(ListAI, fmt.Sprintf("%s(%s)", spares.AI, varName))
			} else if varType == "DO" {
				ListDO = append(ListDO, fullName)
				ListDI = append(ListDI, fmt.Sprintf("%s(%s)", spares.DI, varName))
			}
		}
	}
	return scanner.Err()
}

func generateListsFile() error {
	file, err := os.Create(ListFile)
	if err != nil {
		return err
	}
	defer file.Close()
	w := bufio.NewWriter(file)

	write := func(code, title string, items []string) {
		fmt.Fprintf(w, "*LIST %s   '%s'\n", code, title)
		for _, item := range items {
			fmt.Fprintln(w, item)
		}
		fmt.Fprintln(w, "")
	}

	write("32761", "ENTRADAS ANALOGICAS DNP", ListAI)
	write("32762", "SALIDAS ANALOGICAS DNP", ListAO)
	write("32763", "ENTRADAS DIGITALES DNP", ListDI)
	write("32764", "SALIDAS DIGITALES DNP", ListDO)

	return w.Flush()
}
