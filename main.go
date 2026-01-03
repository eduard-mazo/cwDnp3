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
		SigExtPath  string `yaml:"sigext_path"`
		SigExtFlags string `yaml:"sigext_flags"`
		Spares      struct {
			DO string `yaml:"do"`
			DI string `yaml:"di"`
			AO string `yaml:"ao"`
			AI string `yaml:"ai"`
		} `yaml:"spares"`
	} `yaml:"app"`
}

// Constantes de estructura
const (
	RelativePathToResource = `C\CWave_Micro\R\RTU_RESOURCE`
	ConfigFile             = "config.yaml"
	VarDefFile             = "__vardef.ini"
	ListFile               = "__lists.ini"
)

// Variables Globales
var (
	GlobalConfig                   Config
	ListAO, ListAI, ListDO, ListDI []string
)

func main() {
	// 1. Configurar Logging
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	fmt.Println("--- Generador DNP3 CLI v2.2 (Path Fix) ---")

	// 2. Parsear Argumentos
	projectPathPtr := flag.String("path", "", "Ruta raíz del proyecto")
	nodeNamePtr := flag.String("node", "", "Nombre del Nodo")
	skipExtPtr := flag.Bool("skip-ext", false, "Saltar ejecución de SIGEXT")

	flag.Parse()

	if *projectPathPtr == "" || *nodeNamePtr == "" {
		log.Fatal("Uso incorrecto. Faltan argumentos -path o -node")
	}

	// [FIX CRITICO] Convertir a Ruta Absoluta inmediatamente
	// Esto evita que el os.Chdir rompa las referencias posteriores
	absProjectPath, err := filepath.Abs(*projectPathPtr)
	if err != nil {
		log.Fatalf("Error resolviendo ruta absoluta del proyecto: %v", err)
	}

	// 3. Cargar Configuración
	loadConfiguration()

	// 4. Construir Rutas (Usando ruta absoluta)
	resourceDir := filepath.Join(absProjectPath, RelativePathToResource)
	sigFile := filepath.Join(resourceDir, *nodeNamePtr+".SIG")

	// Buscar MWT (puede estar en raíz o en resource)
	mwtFile := filepath.Join(absProjectPath, *nodeNamePtr+".mwt")
	if _, err := os.Stat(mwtFile); os.IsNotExist(err) {
		mwtFile = filepath.Join(resourceDir, *nodeNamePtr+".mwt")
	}

	log.Printf("Directorio de Recursos: %s", resourceDir)

	// Validar acceso
	if _, err := os.Stat(resourceDir); os.IsNotExist(err) {
		log.Fatalf("[FATAL] Ruta no encontrada: %s", resourceDir)
	}

	// 5. CAMBIAR CONTEXTO (Entrar a la carpeta)
	// Ahora es seguro porque sigFile ya es una ruta absoluta completa
	if err := os.Chdir(resourceDir); err != nil {
		log.Fatalf("[FATAL] No se pudo acceder al directorio: %v", err)
	}

	// 6. Cargar Definiciones (__vardef.ini)
	if _, err := os.Stat(VarDefFile); os.IsNotExist(err) {
		log.Printf("[WARN] No se encontró %s. Se continúa sin validación.", VarDefFile)
	}

	// 7. Lógica SIGEXT vs Fallback
	if !*skipExtPtr {
		log.Println("Intentando ejecutar SIGEXT.exe...")
		err := runSigExt(GlobalConfig.App.SigExtPath, GlobalConfig.App.SigExtFlags, mwtFile, *nodeNamePtr, sigFile)
		if err != nil {
			// Es normal que falle en tu entorno actual, no es Fatal
			log.Printf("[ERROR] SIGEXT falló: %v", err)
			log.Println(">> FALLBACK: Usando archivo .SIG existente <<")
		} else {
			log.Println("SIGEXT completado.")
		}
	}

	// 8. Verificar existencia del .SIG
	if _, err := os.Stat(sigFile); os.IsNotExist(err) {
		// Imprimimos la ruta exacta que falló para depurar
		log.Fatalf("[FATAL] Archivo SIG no encontrado en:\n%s", sigFile)
	}

	// 9. Procesar
	log.Printf("Procesando: %s", filepath.Base(sigFile))
	if err := processSigFile(sigFile); err != nil {
		log.Fatalf("[FATAL] Error procesando: %v", err)
	}

	// 10. Generar
	log.Println("Generando __lists.ini...")
	if err := generateListsFile(); err != nil {
		log.Fatalf("[FATAL] Error escribiendo: %v", err)
	}

	fmt.Println("\n--- RESUMEN FINAL ---")
	fmt.Printf("DI: %d | DO: %d | AI: %d | AO: %d\n", len(ListDI), len(ListDO), len(ListAI), len(ListAO))
	log.Println("Éxito.")

	time.Sleep(1 * time.Second)
}

// --- FUNCIONES ---

func loadConfiguration() {
	exePath, _ := os.Executable()
	configPath := filepath.Join(filepath.Dir(exePath), ConfigFile)

	f, err := os.Open(configPath)
	if err != nil {
		log.Fatalf("No se lee %s: %v", ConfigFile, err)
	}
	defer f.Close()

	if err := yaml.NewDecoder(f).Decode(&GlobalConfig); err != nil {
		log.Fatalf("Error YAML: %v", err)
	}
}

func runSigExt(exePath, flags, mwtPath, nodeName, sigPath string) error {
	if _, err := os.Stat(exePath); os.IsNotExist(err) {
		return fmt.Errorf("ejecutable no encontrado: %s", exePath)
	}

	// Preparar argumentos con flags opcionales
	args := []string{}
	if flags != "" {
		args = append(args, strings.Fields(flags)...)
	}
	args = append(args, mwtPath, nodeName, sigPath)

	cmd := exec.Command(exePath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("err: %v | out: %s", err, string(output))
	}
	return nil
}

func processSigFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	ListAO, ListAI, ListDO, ListDI = []string{}, []string{}, []string{}, []string{}
	spares := GlobalConfig.App.Spares

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

			// --- LÓGICA ESPEJO ---

			if varType == "AAR" || varType == "AA" {
				// Analógicas
				isLIT := strings.Contains(varName, "LIT")
				isHiHi := strings.Contains(varName, "H_H")
				isLoLo := strings.Contains(varName, "L_L")
				isSP := strings.Contains(varName, "_SP")
				isSPAN := strings.Contains(varName, "_SPAN")

				isOutput := (isLIT && isHiHi) || (isLIT && isLoLo) || (isSP && !isSPAN)

				if isOutput {
					ListAO = append(ListAO, fullName)
					ListAI = append(ListAI, fullName) // Espejo I/O
				} else {
					ListAI = append(ListAI, fullName)
					ListAO = append(ListAO, spares.AO) // Spare Salida
				}

			} else if varType == "LA" {
				// Digitales
				isCmd := strings.Contains(varName, "_RESET") ||
					strings.Contains(varName, "_CMD") ||
					strings.Contains(varName, "_WD") ||
					strings.Contains(varName, "_MANUAL") ||
					strings.Contains(varName, "_OUT") ||
					strings.Contains(varName, "_PULSO")

				if isCmd {
					ListDO = append(ListDO, fullName)
					ListDI = append(ListDI, spares.DI) // Spare Entrada
				} else {
					ListDI = append(ListDI, fullName)
					ListDO = append(ListDO, spares.DO) // Spare Salida
				}
			} else if varType == "AO" {
				ListAO = append(ListAO, fullName)
				ListAI = append(ListAI, spares.AI)
			} else if varType == "DO" {
				ListDO = append(ListDO, fullName)
				ListDI = append(ListDI, spares.DI)
			}
		}
	}
	return scanner.Err()
}

func generateListsFile() error {
	// Se crea en el directorio actual (que cambiamos con os.Chdir)
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

	write("32764", "SALIDAS DIGITALES DNP", ListDO)
	write("32763", "ENTRADAS DIGITALES DNP", ListDI)
	write("32762", "SALIDAS ANALOGICAS DNP", ListAO)
	write("32761", "ENTRADAS ANALOGICAS DNP", ListAI)

	return w.Flush()
}
