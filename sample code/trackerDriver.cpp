#include "trackerDriver.h"

//dont forget to define your statics or the linker complains.
int trackerDriver::nMotors;
int trackerDriver::baud = 9600;
std::string trackerDriver::port;
bool trackerDriver::connected;
HANDLE trackerDriver::comPort;
DCB trackerDriver::comParams;
COMMTIMEOUTS trackerDriver::comTimeOuts;
int trackerDriver::selectedComport;
char trackerDriver::readBuffer[1024];
int32_t trackerDriver::registerData[128]{ 0 };
DWORD trackerDriver::readBytes;


static std::vector<int> comPorts;

bool trackerDriver::getComPorts()
{
    char lpTargetPath[1024];
    bool gotPort = false;
    std::cout << "Detecting COM ports.\r\n";
    for (int i = 0; i < 255; i++)
    {
        std::string str = "COM" + std::to_string(i);
        DWORD test = QueryDosDevice(str.c_str(), lpTargetPath, sizeof(lpTargetPath));
        
        if (test != 0)
        {
            std::cout << str << ": " << lpTargetPath << std::endl;
            comPorts.push_back(i);
            gotPort = true;
        }

        if (::GetLastError() == ERROR_INSUFFICIENT_BUFFER)
        {
            std::cout << "What did you even do.\r\n";
        }
    }

    return gotPort;
}

bool trackerDriver::openComPort()
{
    if (connected)                                                      //dont open if already connected
        return true; 

    std::string portname = "COM" + std::to_string(comPorts.at(0));
    portname = "\\\\.\\" + portname;
    std::cout << "Opening " << portname << "\r\n";

    comPort = CreateFile(portname.c_str(), GENERIC_READ | GENERIC_WRITE, 0, 0, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, 0);
    connected = true;

    DCB comParams = { 0 };
    comParams.DCBlength = sizeof(comParams);

    GetCommState(comPort, &comParams);
    comParams.BaudRate = baud;
    std::cout << comParams.BaudRate << " Baud\r\n";
    comParams.ByteSize = 8;
    comParams.StopBits = ONESTOPBIT;
    comParams.Parity = EVENPARITY;
    SetCommState(comPort, &comParams);

    COMMTIMEOUTS comTimeOuts = { 0 };
    comTimeOuts.ReadIntervalTimeout = 20;
    comTimeOuts.ReadTotalTimeoutConstant = 10;
    comTimeOuts.ReadTotalTimeoutMultiplier = 10;
    comTimeOuts.WriteTotalTimeoutConstant = 50;
    comTimeOuts.WriteTotalTimeoutMultiplier = 10;
    SetCommTimeouts(comPort, &comTimeOuts);
    return true;
}
bool trackerDriver::closeComPort()
{
    if (!connected)
        return true;
    
    connected = false;
    CloseHandle(comPort);
    std::cout << "\r\nport closed\r\n\r\n";
    return true;
}

void trackerDriver::sendQuery(int slave, int function, char data[], int dataLength)
{
    char sendBuffer[1024]{ 0 };                                     //create send buffer

    sendBuffer[0] = slave;                                          //Add slave address to packet
    sendBuffer[1] = function;                                       //Add function id to packet

    for (int s = 0; s < dataLength; s++)                            //Add data to packet
        sendBuffer[s + 2] = data[s];

    DWORD crcResult = calculateCRC16(sendBuffer, dataLength + 2);   //Calculate crc for packet
    sendBuffer[dataLength + 2] = crcResult & 0xff;
    sendBuffer[dataLength + 3] = (crcResult >> 8) & 0xff;           //Add the CRC to the packet

    std::cout << "Sent " << dataLength + 4 << " Bytes of Data:\r\n";      //Debug printing
    for (int f = 0; f < dataLength + 4; f++)
    {
        std::cout << "0x" << std::hex << (int)(sendBuffer[f] & 0xff) << " ";
    }
    std::cout << "\r\n";

    if (connected)                                                  //If serial port is open, send the packet.
    {
        WriteFile(comPort, sendBuffer, dataLength + 4, NULL, NULL);

        if (slave > 0)                                              //if its not a broadcast packet get the response.
        {
            if (getResponse() == 0)
                std::cout << "\r\n failed to get response from motor\r\n";
            Sleep(1);
        }
        else                                                        //Add a delay if youre not waiting for a response
            if (baud < 10000)
                Sleep(15);
            else
                Sleep(5);
    }
}

void trackerDriver::testsend()                                      //random test junk. get rid of this for release.
{
    std::cout << "testsend\r\n";
    char sendBuffer1[1024]{ 0x18,0x02,0x00,0x04,0x08,0x00,0x00,0x03,0xe8,0x00,0x00,0x13,0x88 }; //set up motor to move 1000steps
    char sendBuffer2[1024]{ 0x00,0x7d,0x40,0x00}; //run motor
    char sendBuffer3[1024]{ 0x04,0x80,0x00,0x02,0x04,0x00,0x00,0x27,0x10 }; //setup motor to run contionously at 10000hz 9long
    char directdatatest[1024]{ 0x00,0x58,0,0x10,0x20,0,0,0,0,0,0,0,0x1,0,0,0x0,0x0,0,0,0x4e,0x20,0,0,0x5,0xdc,0,0,0x5,0xdc,0,0,0x3,0xe8,0,0,0,0x1 }; //37long
    //stopMotors();

    sendQuery(1, 0x10, directdatatest, 37);
    sendQuery(2, 0x10, directdatatest, 37);
    writeRegister(1, 0x007d, 0x4000);

}

void trackerDriver::stopMotors()
{
    writeRegister(0, 0x007d, 0x0000);
}

void trackerDriver::setMotorSpeed(int slave, int hz)
{
    writeRegister(slave, 0x007d, hz);
}


void trackerDriver::immediatePositionInc(int slave, int32_t position, int32_t speed, int32_t acceleration)
{
    char buff[512]{ 0 };
    int c = 0;
    c += insertIntoBuffer((int32_t)0, buff, c);     //opperation 0
    c += insertIntoBuffer((int32_t)2, buff, c);     //directmode 2 incremental.
    c += insertIntoBuffer(position, buff, c);       //position
    c += insertIntoBuffer(speed, buff, c);          //speed
    c += insertIntoBuffer(acceleration, buff, c);   //acceleration
    c += insertIntoBuffer(acceleration, buff, c);   //deceleration
    c += insertIntoBuffer((int32_t)1000, buff, c);  //current 100.0%
    c += insertIntoBuffer((int32_t)1, buff, c);     //triger on all of this.

    writeMultipleRegisters(slave, 0x0058, 16, buff);
}

void trackerDriver::immediatePositionAbs(int slave, int32_t position, int32_t speed, int32_t acceleration)
{
    char buff[512]{ 0 };
    int c = 0;
    c += insertIntoBuffer((int32_t)0, buff, c);     //opperation 0
    c += insertIntoBuffer((int32_t)1, buff, c);     //directmode 1  absolute.
    c += insertIntoBuffer(position, buff, c);       //position
    c += insertIntoBuffer(speed, buff, c);          //speed
    c += insertIntoBuffer(acceleration, buff, c);   //acceleration
    c += insertIntoBuffer(acceleration, buff, c);   //deceleration
    c += insertIntoBuffer((int32_t)1000, buff, c);  //current 100.0%
    c += insertIntoBuffer((int32_t)1, buff, c);     //triger on all of this.

    writeMultipleRegisters(slave, 0x0058, 16, buff);
}

void trackerDriver::writeRegister(int slave, int reg, int value)
{
    char buff[4];
    buff[1] = reg & 0xff;
    buff[0] = (reg >> 8) & 0xff;
    buff[3] = value & 0xff;
    buff[2] = (value >> 8) & 0xff;

    sendQuery(slave, 0x06, buff, 4);
}

void trackerDriver::writeMultipleRegisters(int slave, int regBase, int nRegisters, char data[])
{
    int dataLength = nRegisters * 2;
    char buff[1024]{ 0 };

    buff[1] = regBase & 0xff;           //register base address two bytes
    buff[0] = (regBase >> 8) & 0xff;    

    buff[3] = nRegisters & 0xff;        //number of registers to be written
    buff[2] = (nRegisters >> 8) & 0xff;

    buff[4] = (nRegisters * 2) & 0xff;  //number of bytes to be written

    for (int i = 0; i < (nRegisters * 2); i++)
        buff[i + 5] = data[i];

    sendQuery(slave, 0x10, buff, dataLength + 5);
}

void trackerDriver::readRegisters(int slave, int regBase, int nRegisters)
{
    char buff[4]{ 0 };
    buff[1] = regBase & 0xff;
    buff[0] = (regBase >> 8) & 0xff;

    buff[3] = nRegisters & 0xff;
    buff[2] = (nRegisters >> 8) & 0xff;

    sendQuery(slave, 3, buff, 4);
    for (int i = 0; i < (nRegisters/2); i++)
        registerData[i] = readFromBuffer(readBuffer, 3 + (i * 4));
}

bool trackerDriver::isMoving(int slave)
{
    readRegisters(slave, 0x007F, 1);
    if ((readBuffer[3] & 0x20))
        return true;
    return false;
}

bool trackerDriver::isInPosition(int slave)
{
    readRegisters(slave, 0x007F, 1);
    if ((readBuffer[3] & 0x40))
        return true;
    return false;
}

void trackerDriver::diagQuery(int slave)
{
    char diagdata[] = {0x00,0x00,0x12,0x34};
    sendQuery(slave, 8, diagdata, 4);
}

int trackerDriver::getResponse()
{
    readBytes = 0;
    
    if (connected)
        ReadFile(comPort,readBuffer,1024,&readBytes,NULL);

    std::cout << "Received "<< std::to_string(readBytes) << " Bytes of Data:\r\n";
    for (int i = 0; i < readBytes; i++)
        std::cout << "0x" << std::hex << (int)(readBuffer[i]&0xff) << " ";
    std::cout << "\r\n";

    return readBytes;
}

int trackerDriver::insertIntoBuffer(int32_t in, char buffer[], int position)
{
    buffer[position + 3] = in & 0xff;
    buffer[position + 2] = (in >> 8) & 0xff;
    buffer[position + 1] = (in >> 16) & 0xff;
    buffer[position] = (in >> 24) & 0xff;

    return 4;
}

int32_t trackerDriver::readFromBuffer(char buffer[], int position)
{
    int32_t out = 0;
    out = buffer[position]&0xff;
    out = out << 8;
    out |= (buffer[position+1]&0xff);
    out = out << 8;
    out |= (buffer[position+2]&0xff);
    out = out << 8;
    out |= (buffer[position+3]&0xff);
    return out;
}

unsigned int trackerDriver::calculateCRC16(char buf[], int len) //MODBUSTOOLS.COM CRC16-modbus Calculator
{
    static const WORD wCRCTable[] = {
    0X0000, 0XC0C1, 0XC181, 0X0140, 0XC301, 0X03C0, 0X0280, 0XC241,
    0XC601, 0X06C0, 0X0780, 0XC741, 0X0500, 0XC5C1, 0XC481, 0X0440,
    0XCC01, 0X0CC0, 0X0D80, 0XCD41, 0X0F00, 0XCFC1, 0XCE81, 0X0E40,
    0X0A00, 0XCAC1, 0XCB81, 0X0B40, 0XC901, 0X09C0, 0X0880, 0XC841,
    0XD801, 0X18C0, 0X1980, 0XD941, 0X1B00, 0XDBC1, 0XDA81, 0X1A40,
    0X1E00, 0XDEC1, 0XDF81, 0X1F40, 0XDD01, 0X1DC0, 0X1C80, 0XDC41,
    0X1400, 0XD4C1, 0XD581, 0X1540, 0XD701, 0X17C0, 0X1680, 0XD641,
    0XD201, 0X12C0, 0X1380, 0XD341, 0X1100, 0XD1C1, 0XD081, 0X1040,
    0XF001, 0X30C0, 0X3180, 0XF141, 0X3300, 0XF3C1, 0XF281, 0X3240,
    0X3600, 0XF6C1, 0XF781, 0X3740, 0XF501, 0X35C0, 0X3480, 0XF441,
    0X3C00, 0XFCC1, 0XFD81, 0X3D40, 0XFF01, 0X3FC0, 0X3E80, 0XFE41,
    0XFA01, 0X3AC0, 0X3B80, 0XFB41, 0X3900, 0XF9C1, 0XF881, 0X3840,
    0X2800, 0XE8C1, 0XE981, 0X2940, 0XEB01, 0X2BC0, 0X2A80, 0XEA41,
    0XEE01, 0X2EC0, 0X2F80, 0XEF41, 0X2D00, 0XEDC1, 0XEC81, 0X2C40,
    0XE401, 0X24C0, 0X2580, 0XE541, 0X2700, 0XE7C1, 0XE681, 0X2640,
    0X2200, 0XE2C1, 0XE381, 0X2340, 0XE101, 0X21C0, 0X2080, 0XE041,
    0XA001, 0X60C0, 0X6180, 0XA141, 0X6300, 0XA3C1, 0XA281, 0X6240,
    0X6600, 0XA6C1, 0XA781, 0X6740, 0XA501, 0X65C0, 0X6480, 0XA441,
    0X6C00, 0XACC1, 0XAD81, 0X6D40, 0XAF01, 0X6FC0, 0X6E80, 0XAE41,
    0XAA01, 0X6AC0, 0X6B80, 0XAB41, 0X6900, 0XA9C1, 0XA881, 0X6840,
    0X7800, 0XB8C1, 0XB981, 0X7940, 0XBB01, 0X7BC0, 0X7A80, 0XBA41,
    0XBE01, 0X7EC0, 0X7F80, 0XBF41, 0X7D00, 0XBDC1, 0XBC81, 0X7C40,
    0XB401, 0X74C0, 0X7580, 0XB541, 0X7700, 0XB7C1, 0XB681, 0X7640,
    0X7200, 0XB2C1, 0XB381, 0X7340, 0XB101, 0X71C0, 0X7080, 0XB041,
    0X5000, 0X90C1, 0X9181, 0X5140, 0X9301, 0X53C0, 0X5280, 0X9241,
    0X9601, 0X56C0, 0X5780, 0X9741, 0X5500, 0X95C1, 0X9481, 0X5440,
    0X9C01, 0X5CC0, 0X5D80, 0X9D41, 0X5F00, 0X9FC1, 0X9E81, 0X5E40,
    0X5A00, 0X9AC1, 0X9B81, 0X5B40, 0X9901, 0X59C0, 0X5880, 0X9841,
    0X8801, 0X48C0, 0X4980, 0X8941, 0X4B00, 0X8BC1, 0X8A81, 0X4A40,
    0X4E00, 0X8EC1, 0X8F81, 0X4F40, 0X8D01, 0X4DC0, 0X4C80, 0X8C41,
    0X4400, 0X84C1, 0X8581, 0X4540, 0X8701, 0X47C0, 0X4680, 0X8641,
    0X8201, 0X42C0, 0X4380, 0X8341, 0X4100, 0X81C1, 0X8081, 0X4040 };

    BYTE nTemp;
    WORD wCRCWord = 0xFFFF;

    while (len--)
    {
        nTemp = *buf++ ^ wCRCWord;
        wCRCWord >>= 8;
        wCRCWord ^= wCRCTable[nTemp];
    }
    return wCRCWord;
}